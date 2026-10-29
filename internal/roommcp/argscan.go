package roommcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode"
)

// Rule names the check that blocked a call. Its text is safe to show an agent:
// it says which kind of data matched and never the value.
type Rule string

const (
	RuleSecret     Rule = "a saved secret"
	RuleTokenShape Rule = "text that looks like a token or key"
	RulePersonal   Rule = "the owner's name, email or account identity"
	RuleBlockTerm  Rule = "a term the owner blocked"
	RuleLogin      Rule = "a GitHub login the owner did not allow for this server"
	RulePath       Rule = "a home or local file path"
	RuleDynaID     Rule = "a Dyna session, room or job ID"
)

// Identities is what the scanner must keep out of every call. The engine reads
// it from the host for each call, so a changed git identity or a new account
// applies at once.
type Identities struct {
	// Personal holds names and email addresses: the git identity of the room's
	// repositories and the identities of the saved accounts.
	Personal []string
	// Logins holds every GitHub login Dyna knows.
	Logins []string
	// Secrets holds saved secret values, such as tokens and API keys.
	Secrets []string
	// Home is the owner's home directory.
	Home string
}

// scanInput is everything one scan needs: the host's identities and the
// owner's per-server choices.
type scanInput struct {
	Identities
	AllowedLogins []string
	BlockTerms    []blockTerm
}

// blockTerm is a stored term: the hash of its case-folded words, and the word
// count the scanner needs to slice text the same way.
type blockTerm struct {
	Hash  string
	Words int
}

var (
	tokenShapes = regexp.MustCompile(`(?i)\b(?:gh[pousr]_[a-z0-9]{20,}|github_pat_[a-z0-9_]{20,}|sk-[a-z0-9_-]{20,}|xox[abpr]-[a-z0-9-]{10,}|akia[0-9a-z]{16}|eyj[a-z0-9_-]{10,}\.[a-z0-9_-]{10,}\.[a-z0-9_-]{5,})`)
	// longRun finds a long unbroken run of key-like characters. entropy decides
	// whether a run is random.
	longRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
	// localPath finds an absolute path in a place where a path starts. A URL
	// path follows a host name, so it never matches.
	localPath = regexp.MustCompile(`(?:^|[\s"'(=,])(?:/(?:home|Users|root|tmp|var|etc|opt|usr|mnt|srv|proc|dev|run)(?:/|\b)|[A-Za-z]:\\)`)
	dynaID    = regexp.MustCompile(`(?i)\b(?:ses|sh|room|msg|run|job)_(?:\d{8}-\d{6}-[0-9a-f]{8}|[0-9a-f]{12,})\b`)
)

const (
	minSecretLength = 8
	minPhraseLength = 3
	// minEntropy is the Shannon entropy, in bits per character, that a long run
	// needs to count as random. Hex digests stay under it.
	minEntropy = 4.3
)

// termHash is the stored form of a block term: case-folded words, hashed, so
// the list is not a second copy of personal data.
func termHash(term string) (hash string, words int) {
	fields := wordsOf(term)
	return phraseHash(fields), len(fields)
}

func phraseHash(words []string) string {
	sum := sha256.Sum256([]byte(strings.Join(words, " ")))
	return hex.EncodeToString(sum[:])
}

// wordsOf splits text into lower-case runs of letters and digits.
func wordsOf(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func entropy(run string) float64 {
	counts := make(map[rune]float64)
	for _, r := range run {
		counts[r]++
	}
	total := float64(len(run))
	bits := 0.0
	for _, n := range counts {
		bits -= n / total * math.Log2(n/total)
	}
	return bits
}

// scanArguments checks every key and string in a call's JSON arguments. It
// returns the first rule that matches, or false when the arguments are clean.
func scanArguments(arguments json.RawMessage, in scanInput) (Rule, bool, error) {
	phrases := make(map[string]Rule)
	counts := make(map[int]bool)
	add := func(text string, rule Rule) {
		words := wordsOf(text)
		if len(strings.Join(words, "")) < minPhraseLength {
			return
		}
		phrases[phraseHash(words)] = rule
		counts[len(words)] = true
	}
	for _, text := range in.Personal {
		add(text, RulePersonal)
	}
	allowed := make(map[string]bool, len(in.AllowedLogins))
	for _, login := range in.AllowedLogins {
		allowed[strings.ToLower(login)] = true
	}
	for _, login := range in.Logins {
		if !allowed[strings.ToLower(login)] {
			add(login, RuleLogin)
		}
	}
	for _, term := range in.BlockTerms {
		phrases[term.Hash] = RuleBlockTerm
		counts[term.Words] = true
	}

	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		text, ok := token.(string)
		if !ok {
			continue
		}
		for _, secret := range in.Secrets {
			if len(secret) >= minSecretLength && strings.Contains(text, secret) {
				return RuleSecret, true, nil
			}
		}
		if tokenShapes.MatchString(text) {
			return RuleTokenShape, true, nil
		}
		for _, run := range longRun.FindAllString(text, -1) {
			if entropy(run) >= minEntropy {
				return RuleTokenShape, true, nil
			}
		}
		words := wordsOf(text)
		for n := range counts {
			for i := 0; i+n <= len(words); i++ {
				if rule, ok := phrases[phraseHash(words[i:i+n])]; ok {
					return rule, true, nil
				}
			}
		}
		if in.Home != "" && strings.Contains(strings.ToLower(text), strings.ToLower(in.Home)) || localPath.MatchString(text) {
			return RulePath, true, nil
		}
		if dynaID.MatchString(text) {
			return RuleDynaID, true, nil
		}
	}
}
