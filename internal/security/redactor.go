package security

import "github.com/tylerdean/package-tracker-demo/internal/telemetry"

// RedactPII scrubs sensitive personal identifiable information (PII) including
// email addresses, phone numbers, street addresses, SSNs, credit cards, and tokens.
func RedactPII(text string) string {
	return telemetry.RedactPII(text)
}

// RedactPIIBytes scrubs PII from raw payload byte slices before storage.
func RedactPIIBytes(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}
	return []byte(telemetry.RedactPII(string(raw)))
}

// RedactEmailPayload processes an inbound email payload, redacting personal PII
// while preserving MIME boundaries, headers, tracking numbers, and delivery dates.
func RedactEmailPayload(filename string, rawBytes []byte) []byte {
	return RedactPIIBytes(rawBytes)
}

// SanitizeLogMap recursively scrubs all string values in a telemetry/logging map
// to guarantee zero PII leaks to Cloud Logging or stdout.
func SanitizeLogMap(m map[string]interface{}) map[string]interface{} {
	return telemetry.SanitizeLogMap(m)
}
