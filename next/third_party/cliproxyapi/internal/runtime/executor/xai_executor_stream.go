package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func (e *XAIExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	if xaiIsSpeechRequest(opts) {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /audio/speech"}
	}
	if xaiInputHasItemType(req.Payload, "compaction_trigger") {
		return e.executeCompactionTriggerStream(ctx, auth, req, opts)
	}

	token, _ := xaiCreds(auth)
	baseURL := xaiChatBaseURL(auth)
	logXAIResolvedBaseURL(ctx, baseURL)

	prepared, err := e.prepareResponsesRequest(ctx, req, opts, true)
	if err != nil {
		return nil, err
	}

	reporter := helps.NewExecutorUsageReporter(ctx, e, prepared.baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	prepared.body = sanitizeXAIInputEncryptedContentForBaseURL(prepared.body, baseURL)
	prepared.body = prepared.finalizePayload(prepared.body)
	reporter.SetTranslatedReasoningEffort(prepared.body, e.Identifier())
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(prepared.body))
	if err != nil {
		return nil, err
	}
	applyXAIChatHeaders(httpReq, auth, token, true, prepared.sessionID, opts.Headers)
	e.recordXAIRequest(ctx, auth, url, httpReq.Header.Clone(), prepared.body)

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("xai executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		return nil, xaiHTTPStatusErr(httpResp.StatusCode, data, httpResp.Header)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stopDeliverySupport := usage.SupportStreamDelivery(ctx)
	go func() {
		defer stopDeliverySupport()
		scanner := helps.NewCodexSSEScanner(ctx, httpResp.Body, 50*1024*1024)
		scanner.SetOnLine(func(line []byte) { helps.AppendAPIResponseChunk(ctx, e.cfg, line) })
		claudeInputTokens := helps.NewClaudeInputTokenState(prepared.from, prepared.to, prepared.responseFormat, prepared.originalPayload)
		var streamUsage helps.StreamUsageBuffer
		var terminalSent bool
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("xai executor: close response body error: %v", errClose)
			}
			close(out) // The consumer can need EOF before it acknowledges delivery.
			if terminalSent {
				if errDelivery, tracked := usage.WaitStreamDelivery(ctx); tracked && errDelivery != nil {
					streamUsage.PublishFailure(ctx, reporter, errDelivery)
					return
				}
				streamUsage.Publish(ctx, reporter)
				reporter.EnsurePublished(ctx)
			} else if errCtx := ctx.Err(); errCtx != nil {
				streamUsage.PublishFailure(ctx, reporter, errCtx)
			}
		}()
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		responseFilter := newXAIInternalXSearchResponseFilter(prepared.filterInternalXSearch, prepared.clientDeclaredTools)
		namespaceRestorer := newXAINamespaceRestorer(prepared.namespaceTools)
		nativeResponses := prepared.responseFormat == sdktranslator.FormatOpenAIResponse
		fail := func(err error) {
			helps.RecordAPIResponseError(ctx, e.cfg, err)
			streamUsage.PublishFailure(ctx, reporter, err)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: err}:
			case <-ctx.Done():
			}
		}
		emitTranslatedLine := func(translatedLine []byte, incomplete bool) (int, bool) {
			lines, errBridge := prepared.applyPatch.Stream(translatedLine)
			if errBridge != nil {
				streamUsage.PublishFailure(ctx, reporter, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			}
			var chunks [][]byte
			for _, line := range lines {
				if bytes.HasPrefix(line, xaiDataTag) {
					eventData := bytes.TrimSpace(line[len(xaiDataTag):])
					switch kind := gjson.GetBytes(eventData, "type").String(); kind {
					case "response.output_item.done":
						xaiCollectOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
					case "response.completed", "response.incomplete", "response.done":
						// Reconstruct only after the bridge has restored dispatcher children.
						eventData = xaiPatchCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
						eventData = xaiNormalizeReasoningSummaryData(eventData)
						if kind == "response.completed" || (kind == "response.done" && xaiDoneEventType(eventData) == "response.completed") {
							// Only completed responses carry replayable terminal state.
							cacheXAIReasoningReplayFromCompleted(ctx, prepared.replayScope, eventData)
						}
						ending := line[len(bytes.TrimRight(line, "\r\n")):]
						line = append(append([]byte("data: "), eventData...), ending...)
					}
				}
				chunks = append(chunks, helps.TranslateStreamWithClaudeInputTokens(ctx, prepared.to, prepared.responseFormat, req.Model, prepared.originalPayload, prepared.body, line, &param, claudeInputTokens)...)
			}
			helps.RecordApplyPatchStreamFailureWithUsage(ctx, param, reporter, &streamUsage, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i], Incomplete: incomplete}:
				case <-ctx.Done():
					return 0, false
				}
			}
			if helps.StopApplyPatchStreamWithUsage(ctx, param, reporter, &streamUsage, out, statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage}) {
				return 0, false
			}
			if errBridge != nil {
				fail(statusErr{code: http.StatusBadGateway, msg: helps.ApplyPatchUpstreamErrorMessage})
				return 0, false
			}
			return len(chunks), true
		}
		var sawData bool
		for eventName, raw := range xaiResponseEvents(scanner) {
			if bytes.Equal(raw, []byte("[DONE]")) {
				if terminalSent {
					emitTranslatedLine([]byte("data: [DONE]"), false)
					return
				}
				break
			}
			if terminalSent {
				continue
			}
			sawData = true
			validJSON := gjson.ValidBytes(raw)
			if validJSON {
				streamUsage.ObserveResponsesStream(raw)
			} else if scanner.Unterminated() && scanner.Err() != nil {
				// An interrupted final line isn't a provider plain-text error.
				break
			}
			if errTerminal, failed := xaiTerminalError(eventName, raw, validJSON, httpResp.Header); failed {
				fail(errTerminal)
				return
			}
			if !validJSON {
				break
			}
			for _, eventData := range xaiNormalizeReasoningSummaryDataEvents(raw) {
				prepared.applyPatch.RememberDispatcherEvent(eventData)
				eventData = namespaceRestorer.restore(eventData)
				if prepared.webSearchAlias != "" {
					eventData = restoreXAIClientWebSearchName(eventData, prepared.webSearchAlias)
				}
				eventData = responseFilter.apply(eventData)
				if len(eventData) == 0 {
					continue
				}
				reporter.ObserveResponseModel(eventData)
				kind := gjson.GetBytes(eventData, "type").String()
				terminalKind := kind
				if kind == "response.done" {
					terminalKind = xaiDoneEventType(eventData)
					if !nativeResponses {
						kind = terminalKind
						eventData, _ = sjson.SetBytes(eventData, "type", kind)
					}
				}
				terminal := terminalKind == "response.completed" || terminalKind == "response.incomplete"
				if eventName != "" {
					if _, ok := emitTranslatedLine(xaiNormalizeReasoningSummaryEventLine([]byte("event: "+eventName), kind), false); !ok {
						return
					}
				}
				line := append([]byte("data: "), eventData...)
				if nativeResponses {
					line = append(line, '\n', '\n')
				}
				emitted, ok := emitTranslatedLine(line, terminalKind == "response.incomplete")
				if !ok {
					return
				}
				terminalSent = terminal && emitted > 0
				if terminalSent && usage.StreamDeliveryTracked(ctx) {
					// The Responses handler has everything it needs to finish; don't
					// make it wait for optional [DONE] or upstream connection closure.
					return
				}
			}
		}
		if ctx.Err() != nil || terminalSent {
			return
		}
		errScan := scanner.Err()
		if errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
		}
		truncErr := statusErr{code: http.StatusBadGateway, msg: xaiIncompleteStreamMessage, streamTruncated: true}
		if !sawData && errScan == nil {
			truncErr.msg = xaiEmptyStreamMessage
		}
		if prepared.applyPatch.HasPatchActivity() {
			finishEvents, errFinish := prepared.applyPatch.FinishStream()
			if errFinish != nil {
				truncErr.msg = helps.ApplyPatchUpstreamErrorMessage
				streamUsage.PublishFailure(ctx, reporter, truncErr)
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
		}
		fail(truncErr)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}
