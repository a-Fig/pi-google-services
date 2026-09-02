package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sombi/pi-google-services/internal/gmail"
)

func TestResolveAttachments_Empty(t *testing.T) {
	gs := &GmailService{}
	atts, err := gs.resolveAttachments(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if atts != nil {
		t.Errorf("expected nil attachments, got %d items", len(atts))
	}
}

func TestResolveAttachments_LocalPath(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "test.txt")
	if err := os.WriteFile(filePath, []byte("hello world"), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	gs := &GmailService{}
	atts, err := gs.resolveAttachments(context.Background(), []attachmentInput{
		{LocalPath: filePath},
	})
	if err != nil {
		t.Fatalf("expected no error, got %+v", err)
	}
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Filename != "test.txt" {
		t.Errorf("filename = %q, want %q", atts[0].Filename, "test.txt")
	}
	if string(atts[0].Data) != "hello world" {
		t.Errorf("data = %q, want %q", string(atts[0].Data), "hello world")
	}
	if atts[0].MimeType != "text/plain; charset=utf-8" {
		t.Errorf("mimeType = %q, want %q", atts[0].MimeType, "text/plain; charset=utf-8")
	}
}

func TestResolveAttachments_DriveFileID_NoDriveAPI(t *testing.T) {
	gs := &GmailService{}
	_, err := gs.resolveAttachments(context.Background(), []attachmentInput{
		{DriveFileID: "abc123"},
	})
	if err == nil {
		t.Fatal("expected error when Drive API not configured")
	}
	if err.Code != -32603 {
		t.Errorf("error code = %d, want -32603", err.Code)
	}
}

func TestResolveAttachments_NeitherPathNorID(t *testing.T) {
	gs := &GmailService{}
	_, err := gs.resolveAttachments(context.Background(), []attachmentInput{
		{},
	})
	if err == nil {
		t.Fatal("expected error for empty attachment")
	}
	if err.Code != -32602 {
		t.Errorf("error code = %d, want -32602", err.Code)
	}
}

func TestResolveAttachments_LocalFileNotFound(t *testing.T) {
	gs := &GmailService{}
	_, err := gs.resolveAttachments(context.Background(), []attachmentInput{
		{LocalPath: "/nonexistent/path/file.txt"},
	})
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if err.Code != -32603 {
		t.Errorf("error code = %d, want -32603", err.Code)
	}
}

func mustResolve(t *testing.T, savePath, filename string) string {
	t.Helper()
	got, err := resolveSavePath(savePath, filename)
	if err != nil {
		t.Fatalf("resolveSavePath(%q, %q): %v", savePath, filename, err)
	}
	return got
}

func TestResolveSavePath_DefaultsToTempDir(t *testing.T) {
	got := mustResolve(t, "", "report.pdf")
	want := filepath.Join(os.TempDir(), "report.pdf")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestResolveSavePath_ExistingDirectory(t *testing.T) {
	tmp := t.TempDir()
	got := mustResolve(t, tmp, "report.pdf")
	want := filepath.Join(tmp, "report.pdf")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestResolveSavePath_ExplicitFile(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "renamed.pdf")
	if got := mustResolve(t, explicit, "report.pdf"); got != explicit {
		t.Errorf("path = %q, want %q", got, explicit)
	}
}

func TestResolveSavePath_TrailingSeparatorTreatedAsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent-dir") + string(os.PathSeparator)
	got := mustResolve(t, dir, "report.pdf")
	want := filepath.Join(strings.TrimSuffix(dir, string(os.PathSeparator)), "report.pdf")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestResolveSavePath_RejectsTraversalFilename(t *testing.T) {
	tmp := t.TempDir()
	got := mustResolve(t, tmp, "../../etc/passwd")
	want := filepath.Join(tmp, "passwd")
	if got != want {
		t.Errorf("path = %q, want %q — sender filename escaped the directory", got, want)
	}
}

func TestResolveSavePath_ExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	got := mustResolve(t, "~/Downloads/", "report.pdf")
	want := filepath.Join(home, "Downloads", "report.pdf")
	if got != want {
		t.Errorf("path = %q, want %q — a literal ~ directory would be created instead", got, want)
	}
}

func TestResolveSavePath_RefusesHiddenDestinations(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	// An injected email talking the model into overwriting a shell profile or
	// dropping a key must not get a write into a dot-path.
	for _, savePath := range []string{"~/.ssh/", "~/.zshrc", filepath.Join(home, ".config", "x.json")} {
		if got, err := resolveSavePath(savePath, "report.pdf"); err == nil {
			t.Errorf("resolveSavePath(%q) allowed %q; expected refusal", savePath, got)
		}
	}
}

func TestResolveSavePath_AllowsHiddenPathsUnderTempDir(t *testing.T) {
	// t.TempDir() sits under the system temp dir, which is exempt.
	dir := filepath.Join(t.TempDir(), ".cache")
	if _, err := resolveSavePath(dir+string(os.PathSeparator), "report.pdf"); err != nil {
		t.Errorf("temp-dir hidden path should be allowed: %v", err)
	}
}

func TestSafeFilename(t *testing.T) {
	cases := map[string]string{
		"report.pdf":            "report.pdf",
		"../../etc/passwd":      "passwd",
		"/absolute/path.txt":    "path.txt",
		`..\..\windows\bad.exe`: "bad.exe",
		"":                      "attachment",
		"..":                    "attachment",
		"/":                     "attachment",
		// A sender must not be able to land a dotfile or smuggle control
		// characters into the name or the attachment listing.
		".zshrc":            "zshrc",
		".ssh":              "ssh",
		"...":               "attachment",
		"re\x00port.pdf":    "report.pdf",
		"line\r\nbreak.txt": "linebreak.txt",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOpenExclusiveNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "invoice.pdf")
	if err := os.WriteFile(dest, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}

	f, got, err := openExclusive(dest)
	if err != nil {
		t.Fatalf("openExclusive: %v", err)
	}
	f.Close()

	want := filepath.Join(dir, "invoice-1.pdf")
	if got != want {
		t.Errorf("second download went to %q, want %q", got, want)
	}
	kept, err := os.ReadFile(dest)
	if err != nil || string(kept) != "original" {
		t.Errorf("the existing file was modified: %q, %v", kept, err)
	}
}

func TestFormatAttachments_Empty(t *testing.T) {
	if got := formatAttachments(nil); got != "" {
		t.Errorf("expected empty string for no attachments, got %q", got)
	}
}

func TestFormatAttachments_ListsIDsAndSizes(t *testing.T) {
	out := formatAttachments([]gmail.AttachmentInfo{
		{AttachmentID: "att-1", Filename: "report.pdf", MimeType: "application/pdf", Size: 2048},
		{AttachmentID: "att-2", Filename: "logo.png", MimeType: "image/png", Size: 512, Inline: true},
	})

	for _, want := range []string{"report.pdf", "att-1", "2.0 KB", "logo.png", "(inline)", "512 B"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestFmtSize(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		512:        "512 B",
		2048:       "2.0 KB",
		3145728:    "3.0 MB",
		2147483648: "2.0 GB",
	}
	for in, want := range cases {
		if got := fmtSize(in); got != want {
			t.Errorf("fmtSize(%d) = %q, want %q", in, got, want)
		}
	}
}
