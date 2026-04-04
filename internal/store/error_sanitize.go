package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

var (
	statusCodePattern     = regexp.MustCompile(`(?i)\bstatus\s+(\d{3})\b`)
	xmlCodePattern        = regexp.MustCompile(`(?i)<Code>\s*([^<\s]+)\s*</Code>`)
	jsonCodePattern       = regexp.MustCompile(`(?i)"code"\s*:\s*"([^"]+)"`)
	xmlRequestIDPattern   = regexp.MustCompile(`(?i)<RequestId>\s*([^<\s]+)\s*</RequestId>`)
	jsonRequestIDPattern  = regexp.MustCompile(`(?i)"request[_-]?id"\s*:\s*"([^"]+)"`)
	looseRequestIDPattern = regexp.MustCompile(`(?i)\brequest[_ -]?id\b[:= ]+([A-Za-z0-9._:-]+)`)
	jsonCFRayPattern      = regexp.MustCompile(`(?i)"cf-ray"\s*:\s*"([^"]+)"`)
	looseCFRayPattern     = regexp.MustCompile(`(?i)\bcf-ray\b[:= ]+([A-Za-z0-9._:-]+)`)
)

func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	return SanitizeErrorText(err.Error())
}

func SanitizeErrorWithDetail(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "request timed out"
	}
	return SanitizeErrorTextWithDetail(err.Error())
}

func SanitizeErrorText(text string) string {
	msg := compactErrorText(text)
	if msg == "" {
		return ""
	}

	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "context canceled") || strings.Contains(lower, "request canceled"):
		return "request canceled"
	case strings.Contains(lower, "deadline exceeded") || strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout"):
		return "request timed out"
	case strings.Contains(lower, "status 401") || strings.Contains(lower, "status 403") || strings.Contains(lower, "authorization") || strings.Contains(lower, "signature"):
		return "authorization failed"
	case strings.Contains(lower, "connection refused") || strings.Contains(lower, "no such host") || strings.Contains(lower, "network is unreachable") || strings.Contains(lower, "dial tcp") || strings.Contains(lower, "connection reset"):
		return "connection failed"
	case strings.Contains(lower, "status 404"):
		return "resource not found"
	case strings.Contains(lower, "status ") || strings.Contains(msg, "\n") || strings.Contains(msg, "http://") || strings.Contains(msg, "https://") || strings.Contains(msg, "<") || len(msg) > 160:
		return "request failed"
	default:
		return msg
	}
}

func SanitizeErrorTextWithDetail(text string) string {
	summary := SanitizeErrorText(text)
	if summary == "" {
		return ""
	}
	detail := SanitizeErrorDetailText(text)
	if detail == "" {
		return summary
	}
	return summary + " (" + detail + ")"
}

func SanitizeErrorDetailText(text string) string {
	msg := compactErrorText(text)
	if msg == "" {
		return ""
	}

	parts := make([]string, 0, 4)
	if status := firstMatch(msg, statusCodePattern); status != "" {
		parts = append(parts, "status="+status)
	}
	if code := firstSanitizedCode(msg, xmlCodePattern, jsonCodePattern); code != "" {
		parts = append(parts, "s3_code="+code)
	}
	if requestID := firstMatch(msg, jsonRequestIDPattern, xmlRequestIDPattern, looseRequestIDPattern); requestID != "" {
		parts = append(parts, "request_id="+requestID)
	}
	if cfRay := firstMatch(msg, jsonCFRayPattern, looseCFRayPattern); cfRay != "" {
		parts = append(parts, "cf_ray="+cfRay)
	}
	return strings.Join(parts, ", ")
}

func compactErrorText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func firstMatch(msg string, patterns ...*regexp.Regexp) string {
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(msg)
		if len(matches) < 2 {
			continue
		}
		if value := sanitizeDiagnosticValue(matches[1]); value != "" {
			return value
		}
	}
	return ""
}

func firstSanitizedCode(msg string, patterns ...*regexp.Regexp) string {
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(msg)
		if len(matches) < 2 {
			continue
		}
		if value := sanitizeDiagnosticCode(matches[1]); value != "" {
			return value
		}
	}
	return ""
}

func sanitizeDiagnosticValue(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.Trim(value, `"'`)
	value = strings.Trim(value, ".,;")
	if value == "" || len(value) > 120 {
		return ""
	}
	for _, ch := range value {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '.' || ch == '_' || ch == ':' || ch == '-' {
			continue
		}
		return ""
	}
	return value
}

func sanitizeDiagnosticCode(raw string) string {
	value := sanitizeDiagnosticValue(raw)
	if value == "" || strings.Contains(value, "_") {
		return ""
	}
	for _, ch := range value {
		if unicode.IsUpper(ch) {
			return value
		}
	}
	return ""
}
