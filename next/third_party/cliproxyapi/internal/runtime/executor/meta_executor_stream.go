package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func (e *MetaExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if errExpand := helps.ExpandResponsesCompactionCapsules(&req, &opts, "meta"); errExpand != nil {
		return nil, statusErr{code: http.StatusBadRequest, msg: errExpand.Error()}
	}
	if helps.HasResponsesCompactionTriggerRequest(req, opts) {
		return e.executeMetaCompactionStream(ctx, auth, req, opts)
	}

	enriched, errAuth := e.ensureAuth(ctx, auth)
	if errAuth != nil {
		return nil, errAuth
	}

	prepared, errPrepare := e.prepareResponsesRequest(ctx, enriched, req, opts, true)
	if errPrepare != nil {
		return nil, errPrepare
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, enriched)
	defer reporter.TrackFailure(ctx, &err)
	reporter.SetTranslatedReasoningEffort(prepared.body, prepared.to.String())

	baseURL, token := metaCreds(enriched)
	if strings.TrimSpace(baseURL) == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "meta executor: missing provider baseURL"}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if errRequest != nil {
		return nil, errRequest
	}
	applyMetaAPIHeaders(httpReq, enriched, token, true, opts.Headers)
	e.recordMetaRequest(ctx, enriched, url, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, enriched, 0)
	httpClient = reporter.TrackHTTPClientRoundTripOnly(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("meta executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return nil, wrapMetaUpstreamError(httpResp.StatusCode, data)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("meta executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		claudeInputTokens := helps.NewClaudeInputTokenState(prepared.from, prepared.to, prepared.responseFormat, prepared.originalPayload)
		var streamUsage helps.StreamUsageBuffer
		nativeResponses := prepared.responseFormat == sdktranslator.FormatOpenAIResponse
		var terminalDelivered bool
		defer func() {
			if errCtx := ctx.Err(); errCtx != nil && !terminalDelivered {
				streamUsage.PublishFailure(ctx, reporter, errCtx)
				return
			}
			streamUsage.Publish(ctx, reporter)
		}()
		fail := func(err error) {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			streamUsage.PublishFailure(ctx, reporter, err)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: err}:
			case <-ctx.Done():
			}
		}
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		emitTranslatedLine := func(translatedLine []byte) bool {
			lines, errBridge := prepared.applyPatch.Stream(translatedLine)
			if errBridge != nil {
				streamUsage.PublishFailure(ctx, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			}
			var chunks [][]byte
			for _, line := range lines {
				chunks = append(chunks, helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, line, &param, claudeInputTokens)...)
			}
			helps.RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, &streamUsage, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			if helps.StopApplyPatchStreamWithUsage(ctx, param, reporter, &streamUsage, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return false
			}
			if errBridge != nil {
				errBridge = statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}
				streamUsage.PublishFailure(ctx, reporter, errBridge)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: errBridge}:
				case <-ctx.Done():
				}
				return false
			}
			return true
		}
		var pendingEventLine []byte
		var filteringSubscriptionUsage bool
		var skipFilteredBlankLine bool
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmedLine := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmedLine, metaEventTag) {
				eventName := bytes.TrimSpace(trimmedLine[len(metaEventTag):])
				if bytes.Equal(eventName, []byte(metaSubscriptionUsageEventType)) {
					if pendingEventLine != nil {
						if !emitTranslatedLine(pendingEventLine) {
							return
						}
						pendingEventLine = nil
					}
					filteringSubscriptionUsage = true
					skipFilteredBlankLine = true
					continue
				}
				filteringSubscriptionUsage = false
				skipFilteredBlankLine = false
				if pendingEventLine != nil {
					if !emitTranslatedLine(pendingEventLine) {
						return
					}
				}
				pendingEventLine = bytes.Clone(line)
				continue
			}
			if !bytes.HasPrefix(trimmedLine, dataTag) {
				if len(trimmedLine) == 0 {
					filteringSubscriptionUsage = false
					if skipFilteredBlankLine {
						skipFilteredBlankLine = false
						continue
					}
				}
				if filteringSubscriptionUsage {
					continue
				}
				skipFilteredBlankLine = false
				if pendingEventLine != nil {
					if !emitTranslatedLine(pendingEventLine) {
						return
					}
					pendingEventLine = nil
				}
				if !emitTranslatedLine(bytes.Clone(line)) {
					return
				}
				continue
			}
			eventData := bytes.TrimSpace(trimmedLine[len(dataTag):])
			if !terminalDelivered && bytes.Equal(eventData, []byte("[DONE]")) {
				break
			}
			eventType := gjson.GetBytes(eventData, "type").String()
			if isMetaSubscriptionUsageEvent(eventType, eventData) {
				helps.ObserveMetaSubscriptionUsage(ctx, eventData)
				pendingEventLine = nil
				filteringSubscriptionUsage = false
				skipFilteredBlankLine = true
				continue
			}
			if filteringSubscriptionUsage {
				if eventType == "" {
					pendingEventLine = nil
					if gjson.ValidBytes(eventData) {
						filteringSubscriptionUsage = false
					}
					skipFilteredBlankLine = true
					continue
				}
				filteringSubscriptionUsage = false
			}
			skipFilteredBlankLine = false
			// Observe upstream content before translation or failure publication.
			helps.ObserveResponsesTokenEvent(reporter, eventData)
			if eventType == "response.output_item.done" {
				xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			}
			if detail, ok := helps.ParseCodexUsageWithOutputItems(eventData, outputItemsByIndex, outputItemsFallback); ok {
				streamUsage.Observe(detail, true)
			}
			if errEvent := metaStreamEventError(eventData); errEvent != nil {
				fail(errEvent)
				return
			}
			terminal := isMetaTerminalResponseEvent(eventType) && gjson.ValidBytes(eventData)
			if terminal {
				eventData = patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
				if eventType == "response.done" && !nativeResponses {
					var normalizedType string
					normalizedType, eventData = normalizeMetaTerminalEvent(eventData, eventType)
					if bytes.Equal(bytes.TrimSpace(bytes.TrimPrefix(bytes.TrimSpace(pendingEventLine), metaEventTag)), []byte(eventType)) {
						pendingEventLine = []byte("event: " + normalizedType)
					}
				}
			}
			if pendingEventLine != nil {
				if !emitTranslatedLine(pendingEventLine) {
					return
				}
				pendingEventLine = nil
			}
			dataLine := append([]byte("data: "), eventData...)
			if terminal && nativeResponses {
				// Flush the native terminal frame even if Meta leaves the body
				// open to send subscription usage later.
				dataLine = append(dataLine, '\n', '\n')
			}
			if !emitTranslatedLine(dataLine) {
				return
			}
			if terminal {
				terminalDelivered = true
			}
		}
		if ctx.Err() != nil {
			return
		}
		if !terminalDelivered && !prepared.applyPatch.HasPatchActivity() {
			msg := metaIncompleteStreamMessage
			if errScan := scanner.Err(); errScan != nil {
				msg = fmt.Sprintf("%s: %v", msg, errScan)
			}
			fail(statusErr{code: http.StatusBadGateway, msg: msg, streamTruncated: true})
			return
		}
		if pendingEventLine != nil {
			if !emitTranslatedLine(pendingEventLine) {
				return
			}
			pendingEventLine = nil
		}
		finishEvents, errFinish := prepared.applyPatch.FinishStream()
		patchErr := statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage, streamTruncated: !terminalDelivered}
		if errFinish != nil {
			streamUsage.PublishFailure(ctx, reporter, patchErr)
		}
		for _, event := range finishEvents {
			for _, chunk := range helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, event, &param, claudeInputTokens) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errFinish != nil {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: patchErr}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
