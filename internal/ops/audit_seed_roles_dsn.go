package ops

import (
	"net/url"
	"strings"
)

// declaredOperatorPassword checks the final password field before pgx can
// consult PostgreSQL environment variables or a passfile.
func declaredOperatorPassword(dsn string) bool {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return declaredURIPassword(dsn)
	}

	return declaredKeywordPassword(dsn)
}

func declaredURIPassword(dsn string) bool {
	_, rest, _ := strings.Cut(dsn, "://")
	declared := false
	if index := strings.IndexAny(rest, "@/"); index >= 0 && rest[index] == '@' {
		_, password, present := strings.Cut(rest[:index], ":")
		if present {
			decoded, err := url.PathUnescape(strings.Trim(password, " "))
			declared = err == nil && decoded != ""
		}

		rest = rest[index+1:]
	}

	bracketed := false
	path := false
	hostStart := true
	query := ""

	for index := 0; index < len(rest); index++ {
		switch rest[index] {
		case '[':
			if !path && hostStart {
				bracketed = true
			}

			hostStart = false
		case ']':
			bracketed = false
			hostStart = false
		case ',':
			if !path && !bracketed {
				hostStart = true
			}
		case '/':
			if !bracketed {
				path = true
			}
		case '?':
			if !bracketed {
				query = rest[index+1:]
				index = len(rest)
			}
		default:
			hostStart = false
		}
	}

	for pair := range strings.SplitSeq(query, "&") {
		key, value, present := strings.Cut(pair, "=")
		if !present {
			continue
		}

		decodedKey, err := url.PathUnescape(strings.Trim(key, " "))
		if err != nil || decodedKey != "password" {
			continue
		}

		decodedValue, err := url.PathUnescape(strings.Trim(value, " "))
		declared = err == nil && decodedValue != ""
	}

	return declared
}

func declaredKeywordPassword(dsn string) bool {
	const spaces = " \t\n\r\v\f"
	declared := false

	dsn = strings.TrimLeft(dsn, spaces)
	for dsn != "" {
		index := strings.IndexByte(dsn, '=')
		if index < 0 {
			return false
		}

		key := strings.Trim(dsn[:index], spaces)
		if key == "" || strings.ContainsAny(key, spaces) {
			return false
		}

		dsn = strings.TrimLeft(dsn[index+1:], spaces)
		quoted := strings.HasPrefix(dsn, "'")
		if quoted {
			dsn = dsn[1:]
		}

		var value strings.Builder
		closed := !quoted

		index = 0
		for index < len(dsn) {
			character := dsn[index]
			if quoted && character == '\'' {
				closed = true
				index++
				break
			}

			if !quoted && strings.ContainsRune(spaces, rune(character)) {
				break
			}

			if character == '\\' {
				index++
				if index == len(dsn) {
					break
				}

				character = dsn[index]
			}

			value.WriteByte(character)
			index++
		}

		if !closed {
			return false
		}

		if key == "password" {
			declared = value.Len() != 0
		}

		dsn = strings.TrimLeft(dsn[index:], spaces)
	}

	return declared
}
