package worker

import (
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// MessagePreviewAccumulator turns provider text deltas into bounded-rate,
// cumulative visible-prose snapshots. It is intended to be owned by the one
// goroutine translating a provider session.
type MessagePreviewAccumulator struct {
	contract      OutputContract
	interval      time.Duration
	streamID      string
	raw           strings.Builder
	lastText      string
	lastPublished time.Time
}

func NewMessagePreviewAccumulator(
	contract OutputContract,
	interval time.Duration,
) *MessagePreviewAccumulator {
	return &MessagePreviewAccumulator{contract: contract, interval: interval}
}

func (accumulator *MessagePreviewAccumulator) Add(
	streamID string,
	delta string,
	now time.Time,
) (MessagePreview, bool) {
	if accumulator == nil || PreviewProseField(accumulator.contract) == "" ||
		strings.TrimSpace(streamID) == "" || delta == "" {
		return MessagePreview{}, false
	}
	if accumulator.streamID != streamID {
		accumulator.streamID = streamID
		accumulator.raw.Reset()
		accumulator.lastText = ""
		accumulator.lastPublished = time.Time{}
	}
	accumulator.raw.WriteString(delta)
	text := ExtractMessagePreview(accumulator.contract, []byte(accumulator.raw.String()))
	if strings.TrimSpace(text) == "" || text == accumulator.lastText {
		return MessagePreview{}, false
	}
	if !accumulator.lastPublished.IsZero() && now.Sub(accumulator.lastPublished) < accumulator.interval {
		return MessagePreview{}, false
	}
	accumulator.lastText = text
	accumulator.lastPublished = now
	return MessagePreview{StreamID: streamID, Text: text}, true
}

// PreviewProseField returns the one human-visible string field for a structured
// output contract. An empty result means the contract has no streamable final
// prose.
func PreviewProseField(contract OutputContract) string {
	switch contract {
	case OutputContractGoalClarification, OutputContractToolchainSetup:
		return "message"
	case OutputContractPlanningLead:
		return "content"
	case OutputContractImplementationLead,
		OutputContractImplementationReview,
		OutputContractImplementationReadiness:
		return "summary"
	case OutputContractIntervention:
		return "response"
	default:
		return ""
	}
}

// ExtractMessagePreview reads the visible prose prefix from a complete or
// truncated structured-output object. It is deliberately best-effort: the
// completed response still passes through ResolveStructuredOutput. Only a
// top-level string field is considered, and incomplete escapes or UTF-8 runes
// are withheld until a later cumulative buffer makes them complete.
func ExtractMessagePreview(contract OutputContract, raw []byte) string {
	target := PreviewProseField(contract)
	if target == "" {
		return ""
	}
	index := skipPreviewSpace(raw, 0)
	if index >= len(raw) || raw[index] != '{' {
		return ""
	}
	index++
	for {
		index = skipPreviewSpace(raw, index)
		if index >= len(raw) || raw[index] == '}' {
			return ""
		}
		key, next, complete, valid := readPreviewJSONString(raw, index)
		if !valid || !complete {
			return ""
		}
		index = skipPreviewSpace(raw, next)
		if index >= len(raw) || raw[index] != ':' {
			return ""
		}
		index = skipPreviewSpace(raw, index+1)
		if key == target {
			value, _, _, valid := readPreviewJSONString(raw, index)
			if !valid {
				return ""
			}
			return value
		}
		next, complete = skipPreviewJSONValue(raw, index)
		if !complete {
			return ""
		}
		index = skipPreviewSpace(raw, next)
		if index >= len(raw) || raw[index] != ',' {
			return ""
		}
		index++
	}
}

func skipPreviewSpace(raw []byte, index int) int {
	for index < len(raw) && strings.ContainsRune(" \t\r\n", rune(raw[index])) {
		index++
	}
	return index
}

// readPreviewJSONString decodes one JSON string. complete is false when input
// ends before its closing quote; the returned value is still the safely
// decodable prefix in that case.
func readPreviewJSONString(raw []byte, start int) (value string, next int, complete bool, valid bool) {
	if start >= len(raw) || raw[start] != '"' {
		return "", start, false, false
	}
	var decoded strings.Builder
	for index := start + 1; index < len(raw); {
		character := raw[index]
		switch {
		case character == '"':
			return decoded.String(), index + 1, true, true
		case character == '\\':
			if index+1 >= len(raw) {
				return decoded.String(), len(raw), false, true
			}
			escape := raw[index+1]
			switch escape {
			case '"', '\\', '/':
				decoded.WriteByte(escape)
				index += 2
			case 'b':
				decoded.WriteByte('\b')
				index += 2
			case 'f':
				decoded.WriteByte('\f')
				index += 2
			case 'n':
				decoded.WriteByte('\n')
				index += 2
			case 'r':
				decoded.WriteByte('\r')
				index += 2
			case 't':
				decoded.WriteByte('\t')
				index += 2
			case 'u':
				first, after, ready, ok := readPreviewHexRune(raw, index)
				if !ok {
					return "", index, false, false
				}
				if !ready {
					return decoded.String(), len(raw), false, true
				}
				if utf16.IsSurrogate(first) {
					if first < 0xD800 || first > 0xDBFF {
						return "", index, false, false
					}
					second, final, secondReady, secondOK := readPreviewHexRune(raw, after)
					if !secondOK {
						return "", index, false, false
					}
					if !secondReady {
						return decoded.String(), len(raw), false, true
					}
					combined := utf16.DecodeRune(first, second)
					if combined == utf8.RuneError {
						return "", index, false, false
					}
					decoded.WriteRune(combined)
					index = final
					continue
				}
				decoded.WriteRune(first)
				index = after
			default:
				return "", index, false, false
			}
		case character < 0x20:
			return "", index, false, false
		default:
			runeValue, size := utf8.DecodeRune(raw[index:])
			if runeValue == utf8.RuneError && size == 1 {
				if !utf8.FullRune(raw[index:]) {
					return decoded.String(), len(raw), false, true
				}
				return "", index, false, false
			}
			decoded.WriteRune(runeValue)
			index += size
		}
	}
	return decoded.String(), len(raw), false, true
}

func readPreviewHexRune(raw []byte, slash int) (rune, int, bool, bool) {
	if slash >= len(raw) {
		return 0, len(raw), false, true
	}
	if raw[slash] != '\\' {
		return 0, slash, false, false
	}
	if slash+1 >= len(raw) {
		return 0, len(raw), false, true
	}
	if raw[slash+1] != 'u' {
		return 0, slash, false, false
	}
	if slash+6 > len(raw) {
		return 0, len(raw), false, true
	}
	var value rune
	for _, digit := range raw[slash+2 : slash+6] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += rune(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += rune(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += rune(digit-'A') + 10
		default:
			return 0, slash, false, false
		}
	}
	return value, slash + 6, true, true
}

func skipPreviewJSONValue(raw []byte, start int) (int, bool) {
	if start >= len(raw) {
		return start, false
	}
	if raw[start] == '"' {
		_, next, complete, valid := readPreviewJSONString(raw, start)
		return next, complete && valid
	}
	if raw[start] != '{' && raw[start] != '[' {
		for index := start; index < len(raw); index++ {
			if raw[index] == ',' || raw[index] == '}' {
				return index, index > start
			}
		}
		return len(raw), false
	}
	stack := []byte{raw[start]}
	for index := start + 1; index < len(raw); {
		switch raw[index] {
		case '"':
			_, next, complete, valid := readPreviewJSONString(raw, index)
			if !complete || !valid {
				return len(raw), false
			}
			index = next
		case '{', '[':
			stack = append(stack, raw[index])
			index++
		case '}', ']':
			opening := stack[len(stack)-1]
			if (opening == '{' && raw[index] != '}') || (opening == '[' && raw[index] != ']') {
				return index, false
			}
			stack = stack[:len(stack)-1]
			index++
			if len(stack) == 0 {
				return index, true
			}
		default:
			index++
		}
	}
	return len(raw), false
}
