package store

import "testing"

func TestSanitizeErrorTextRedactsBackendBodies(t *testing.T) {
	input := "list objects status 403: <Error><Code>SignatureDoesNotMatch</Code></Error>"
	if got := SanitizeErrorText(input); got != "authorization failed" {
		t.Fatalf("expected authorization failure summary, got %q", got)
	}
}

func TestSanitizeErrorTextKeepsSafeShortMessages(t *testing.T) {
	if got := SanitizeErrorText("enqueue unavailable"); got != "enqueue unavailable" {
		t.Fatalf("expected safe short error to remain visible, got %q", got)
	}
}

func TestSanitizeErrorDetailTextExtractsSafeDiagnostics(t *testing.T) {
	input := `list objects status 403: <Error><Code>SignatureDoesNotMatch</Code><RequestId>2f6f1f4b6d7748f2</RequestId></Error>`
	if got := SanitizeErrorDetailText(input); got != "status=403, s3_code=SignatureDoesNotMatch, request_id=2f6f1f4b6d7748f2" {
		t.Fatalf("expected structured safe diagnostics, got %q", got)
	}
}

func TestSanitizeErrorTextWithDetailCombinesSummaryAndDiagnostics(t *testing.T) {
	input := `queue startup check status 403: <Error><Code>AccessDenied</Code><RequestId>019d5685-077f-70cb-a508-03e04012bbba</RequestId></Error>`
	if got := SanitizeErrorTextWithDetail(input); got != "authorization failed (status=403, s3_code=AccessDenied, request_id=019d5685-077f-70cb-a508-03e04012bbba)" {
		t.Fatalf("expected summary with safe detail, got %q", got)
	}
}
