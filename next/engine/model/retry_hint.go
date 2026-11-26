package model

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// PiG reports model failures to the harness as status and JSON body, not
// headers. Copy a known reset into the error body of the box route while
// retaining valid Retry-After values for PiG's own provider retry policy.
func preserveModelRetryHint(resp *http.Response) error {
	limit := int64(contract.MaxModelRetryAfterSeconds)
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		limit = contract.MaxModelQuotaResetSeconds
	case http.StatusBadGateway, http.StatusServiceUnavailable:
	default:
		return nil
	}
	raw := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if raw == "" || resp.Header.Get("Content-Encoding") != "" {
		return nil
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		date, err := http.ParseTime(raw)
		if err != nil {
			return nil
		}
		delay := time.Until(date)
		if delay <= 0 || delay > time.Duration(limit)*time.Second {
			return nil
		}
		seconds = int64((delay + time.Second - 1) / time.Second)
	}
	if seconds <= 0 || seconds > limit {
		return nil
	}
	const maxErrorBody = 16 << 10
	if resp.ContentLength > maxErrorBody {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody+1))
	if err != nil {
		return err
	}
	if len(body) > maxErrorBody {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(body), resp.Body), resp.Body}
		return nil
	}
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))

	var envelope, detail map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) == nil {
		_ = json.Unmarshal(envelope["error"], &detail)
	}
	if detail == nil {
		detail, envelope = envelope, nil
	}
	if seconds > contract.MaxModelRetryAfterSeconds {
		// PiG can report Retry-After without the JSON code when it exceeds
		// its own retry cap. Do not forward an out-of-range transient hint.
		code := strings.Trim(string(detail["code"]), `"`)
		var message string
		_ = json.Unmarshal(detail["message"], &message)
		if code == "1302" || code == "1305" || code == "transient_rate_limit" ||
			message == "Rate limit reached for requests" || message == "The service may be temporarily overloaded" {
			resp.Header.Del("Retry-After")
			return nil
		}
	}
	if detail == nil {
		text := strings.TrimSpace(string(body))
		if text == "" {
			text = http.StatusText(resp.StatusCode)
		}
		message, _ := json.Marshal(text)
		detail = map[string]json.RawMessage{"message": message}
	}
	if envelope == nil {
		envelope = make(map[string]json.RawMessage)
	}
	if len(detail["message"]) == 0 && json.Valid(envelope["message"]) {
		detail["message"] = envelope["message"]
	}
	delete(envelope, "message") // Anthropic drops the body if a top-level message is present.
	detail["reset_seconds"] = json.RawMessage(strconv.FormatInt(seconds, 10))
	envelope["error"], err = json.Marshal(detail)
	if err != nil {
		return err
	}
	body, err = json.Marshal(envelope)
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	resp.Header.Set("Content-Type", "application/json")
	return nil
}
