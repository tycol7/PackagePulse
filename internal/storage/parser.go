package storage

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	htmlTagRegex  = regexp.MustCompile(`<[^>]*>`)
	spaceRegex    = regexp.MustCompile(`\s+`)
	tomorrowRegex = regexp.MustCompile(`(?i)\b(deliver(ed|ing)?\s+tomorrow|arriv(e|ed|ing)?\s+tomorrow|delivery\s+tomorrow|expected\s+tomorrow|tomorrow\s+by|by\s+tomorrow|\btomorrow\b)`)
	todayRegex    = regexp.MustCompile(`(?i)\b(deliver(ed|ing)?\s+today|arriv(e|ed|ing)?\s+today|delivery\s+today|expected\s+today|out\s+for\s+delivery\s+today|\btoday\b)`)
)

// ParsedEmail contains extracted plaintext representations of an email.
type ParsedEmail struct {
	Subject string
	From    string
	Body    string
	Date    string     // Raw date header from email
	SentAt  *time.Time // Parsed timestamp of when the email was sent
}

// DateString returns a friendly formatted string of the sent date, or "Not specified" if missing.
func (e *ParsedEmail) DateString() string {
	if e.SentAt != nil {
		return e.SentAt.Format("2006-01-02 15:04:05 MST")
	}
	if e.Date != "" {
		return e.Date
	}
	return "Not specified"
}

// HasTomorrowDelivery checks if the email text specifies delivery tomorrow.
func HasTomorrowDelivery(text string) bool {
	return tomorrowRegex.MatchString(text)
}

// HasTodayDelivery checks if the email text specifies delivery today.
func HasTodayDelivery(text string) bool {
	return todayRegex.MatchString(text)
}

// DeduceDeliveryDate deduces the delivery date (YYYY-MM-DD) based on relative wording and the sent timestamp.
// If sentAt is nil or the email does not specify relative delivery, it returns "" without guessing or defaulting to current time.
func DeduceDeliveryDate(text string, sentAt *time.Time) string {
	if sentAt == nil {
		return ""
	}

	if HasTomorrowDelivery(text) {
		return sentAt.AddDate(0, 0, 1).Format("2006-01-02")
	}
	if HasTodayDelivery(text) {
		return sentAt.Format("2006-01-02")
	}
	return ""
}

// ExtractTextFromPayload processes .eml or .txt raw bytes into a clean ParsedEmail.
func ExtractTextFromPayload(filename string, rawBytes []byte) ParsedEmail {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == ".eml" {
		return parseEML(rawBytes)
	}
	return parseTXT(rawBytes)
}

func parseEmailDate(dateHeader string) (string, *time.Time) {
	trimmed := strings.TrimSpace(dateHeader)
	if trimmed == "" {
		return "", nil
	}

	// 1. Standard RFC 5322 date
	if t, err := mail.ParseDate(trimmed); err == nil {
		utc := t.UTC()
		return trimmed, &utc
	}

	// 2. Common date formats
	formats := []string{
		time.RFC1123Z,
		time.RFC1123,
		time.RFC822Z,
		time.RFC822,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02",
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 02 Jan 2006 15:04:05 MST",
		"Jan 2, 2006",
		"January 2, 2006",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, trimmed); err == nil {
			utc := t.UTC()
			return trimmed, &utc
		}
	}

	return trimmed, nil
}

func parseEML(raw []byte) ParsedEmail {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		// Fallback to raw text if MIME header parsing fails
		return parseTXT(raw)
	}

	subject := msg.Header.Get("Subject")
	from := msg.Header.Get("From")
	dateHeader := msg.Header.Get("Date")
	rawDate, sentAt := parseEmailDate(dateHeader)

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		bodyBytes, _ := io.ReadAll(msg.Body)
		clean := cleanBody(string(bodyBytes), mediaType)
		return ParsedEmail{
			Subject: subject,
			From:    from,
			Body:    clean,
			Date:    rawDate,
			SentAt:  sentAt,
		}
	}

	// Handle multipart
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var plainText, htmlText string

	for {
		p, err := mr.NextPart()
		if err != nil {
			break
		}
		partMediaType, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		partBytes, _ := io.ReadAll(p)

		if strings.HasPrefix(partMediaType, "text/plain") && plainText == "" {
			plainText = string(partBytes)
		} else if strings.HasPrefix(partMediaType, "text/html") && htmlText == "" {
			htmlText = string(partBytes)
		}
	}

	var body string
	if plainText != "" {
		body = plainText
	} else if htmlText != "" {
		body = stripHTML(htmlText)
	}

	return ParsedEmail{
		Subject: subject,
		From:    from,
		Body:    strings.TrimSpace(body),
		Date:    rawDate,
		SentAt:  sentAt,
	}
}

func parseTXT(raw []byte) ParsedEmail {
	content := string(raw)
	lines := strings.Split(content, "\n")
	var subject, from, date string
	var bodyLines []string

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "subject:") && subject == "" {
			subject = strings.TrimSpace(trimmed[8:])
		} else if strings.HasPrefix(lower, "from:") && from == "" {
			from = strings.TrimSpace(trimmed[5:])
		} else if strings.HasPrefix(lower, "date:") && date == "" {
			date = strings.TrimSpace(trimmed[5:])
		} else {
			bodyLines = append(bodyLines, lines[i])
		}
	}

	rawDate, sentAt := parseEmailDate(date)
	body := strings.Join(bodyLines, "\n")
	return ParsedEmail{
		Subject: subject,
		From:    from,
		Body:    strings.TrimSpace(body),
		Date:    rawDate,
		SentAt:  sentAt,
	}
}

func cleanBody(body, mediaType string) string {
	if strings.Contains(mediaType, "html") {
		return stripHTML(body)
	}
	return strings.TrimSpace(body)
}

func stripHTML(htmlStr string) string {
	stripped := htmlTagRegex.ReplaceAllString(htmlStr, " ")
	return strings.TrimSpace(spaceRegex.ReplaceAllString(stripped, " "))
}
