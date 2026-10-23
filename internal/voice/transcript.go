package voice

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// TranscriptionPrompt lists the words the transcription model should expect,
// so "root" is not written "route". It is keywords, not prose: on a short
// burst of noise the model may repeat its prompt as the owner's line, and a
// keyword list reads harmlessly if it does.
const TranscriptionPrompt = "Dyna, root, agent, PR, CI, TUI, worktree, rebase, merge queue, qwen."

// transcriptionHallucinations are lines OpenAI's transcription model writes
// for noise, not speech. A call recorded this one for 604 ms of input 73 s
// after Voice stopped speaking, so it was not echo.
var transcriptionHallucinations = []string{"ChatGPT, a large language model trained by OpenAI."}

// hallucinated reports a transcript line the model wrote for noise: a known
// hallucination, or its own prompt repeated, whole or in part, which a call
// recorded for 781 ms of input. A line that shares a run of three or more
// words with the prompt, in either direction, is the prompt; a real line
// that only names Dyna or root is not.
func hallucinated(text string) bool {
	if slices.Contains(transcriptionHallucinations, text) {
		return true
	}
	line := " " + strings.Join(words(text), " ") + " "
	prompt := words(TranscriptionPrompt)
	for i := 0; i+3 <= len(prompt); i++ {
		if strings.Contains(line, " "+strings.Join(prompt[i:i+3], " ")+" ") {
			return true
		}
	}
	return false
}

// words splits text into lowercase words, dropping punctuation.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

var (
	markdownLink   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	markdownLine   = regexp.MustCompile(`(?m)^[ \t]*(?:#{1,6}[ \t]+|>[ \t]?|[-*+][ \t]+|\d+[.)][ \t]+|` + "```" + `.*$)`)
	markdownEmph   = regexp.MustCompile(`\*\*|__|~~|[*` + "`" + `]`)
	markdownBreaks = regexp.MustCompile(`[ \t]*\n+[ \t]*`)
)

// plainSpeech removes markdown from root's text before Voice sees it, so
// labels and markers such as "**Key point:**" or "- " are never read aloud.
func plainSpeech(text string) string {
	text = markdownLink.ReplaceAllString(text, "$1")
	text = markdownLine.ReplaceAllString(text, "")
	text = markdownEmph.ReplaceAllString(text, "")
	return strings.TrimSpace(markdownBreaks.ReplaceAllString(text, " "))
}
