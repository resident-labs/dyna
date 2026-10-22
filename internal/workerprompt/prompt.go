package workerprompt

import "strings"

const workerContextStart = "\n\n[DYNA WORKER CONTEXT]\n"
const workerContextEnd = "\n[/DYNA WORKER CONTEXT]"

// WithWorkerContext keeps variable worker metadata after the reusable prompt.
func WithWorkerContext(prompt, context string) string {
	return prompt + workerContextStart + context + workerContextEnd
}

// AppendPromptSuffix keeps output instructions and corrections before a
// trailing worker context. Prompts without that context keep append semantics.
func AppendPromptSuffix(prompt, suffix string) string {
	if strings.HasSuffix(prompt, workerContextEnd) {
		if at := strings.LastIndex(prompt, workerContextStart); at >= 0 {
			return prompt[:at] + suffix + prompt[at:]
		}
	}
	return prompt + suffix
}
