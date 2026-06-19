package iterm2

import (
	"testing"

	"github.com/champly/mecha/pkg/term/iterm2/api"
	"google.golang.org/protobuf/proto"
)

func TestSplitPaneRequest(t *testing.T) {
	dir := api.SplitPaneRequest_VERTICAL
	req := &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_SplitPaneRequest{
			SplitPaneRequest: &api.SplitPaneRequest{
				Session:        proto.String("abc"),
				SplitDirection: &dir,
			},
		},
	}
	req.Id = proto.Int64(1)
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("empty marshal")
	}
}

func TestSendTextRequest(t *testing.T) {
	req := &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_SendTextRequest{
			SendTextRequest: &api.SendTextRequest{
				Session: proto.String("session-1"),
				Text:    proto.String("hello\n"),
			},
		},
	}
	req.Id = proto.Int64(2)
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = data
}

func TestGetBufferRequest(t *testing.T) {
	req := &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_GetBufferRequest{
			GetBufferRequest: &api.GetBufferRequest{
				Session: proto.String("session-1"),
			},
		},
	}
	req.Id = proto.Int64(3)
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = data
}

func TestCloseRequest(t *testing.T) {
	req := &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_CloseRequest{
			CloseRequest: &api.CloseRequest{
				Target: &api.CloseRequest_Sessions{
					Sessions: &api.CloseRequest_CloseSessions{
						SessionIds: []string{"a", "b"},
					},
				},
			},
		},
	}
	req.Id = proto.Int64(4)
	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = data
}

func TestRoundTripSplitPane(t *testing.T) {
	dir := api.SplitPaneRequest_VERTICAL
	req := &api.ClientOriginatedMessage{
		Submessage: &api.ClientOriginatedMessage_SplitPaneRequest{
			SplitPaneRequest: &api.SplitPaneRequest{
				Session:        proto.String("active"),
				SplitDirection: &dir,
			},
		},
	}
	req.Id = proto.Int64(5)

	data, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	// Unmarshal and verify
	got := &api.ClientOriginatedMessage{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatal(err)
	}
	if got.GetId() != 5 {
		t.Errorf("id = %d, want 5", got.GetId())
	}
	spr := got.GetSplitPaneRequest()
	if spr == nil {
		t.Fatal("SplitPaneRequest is nil")
	}
	if spr.GetSession() != "active" {
		t.Errorf("session = %q, want active", spr.GetSession())
	}
	if spr.GetSplitDirection() != api.SplitPaneRequest_VERTICAL {
		t.Error("expected VERTICAL")
	}
}

func TestRoundTripGetBufferResponse(t *testing.T) {
	// Build a GetBufferResponse with content
	resp := &api.ServerOriginatedMessage{
		Id: proto.Int64(7),
		Submessage: &api.ServerOriginatedMessage_GetBufferResponse{
			GetBufferResponse: &api.GetBufferResponse{
				Status: api.GetBufferResponse_OK.Enum(),
				Contents: []*api.LineContents{
					{Text: proto.String("hello\n")},
					{Text: proto.String("world\n")},
				},
			},
		},
	}

	data, err := proto.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}

	got := &api.ServerOriginatedMessage{}
	if err := proto.Unmarshal(data, got); err != nil {
		t.Fatal(err)
	}
	if got.GetId() != 7 {
		t.Errorf("id = %d, want 7", got.GetId())
	}
	br := got.GetGetBufferResponse()
	if br == nil {
		t.Fatal("GetBufferResponse is nil")
	}
	if len(br.GetContents()) != 2 {
		t.Fatalf("contents len = %d, want 2", len(br.GetContents()))
	}
	if br.GetContents()[1].GetText() != "world\n" {
		t.Errorf("line[1] = %q", br.GetContents()[1].GetText())
	}
}

func TestCookieFormat(t *testing.T) {
	// Just ensure the osascript error format works
	out := "abc123def456789012345678901234ab 11223344-5566-7788-9900-AABBCCDDEEFF"
	cookie, key, err := parseCookie(out)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "abc123def456789012345678901234ab" {
		t.Errorf("cookie = %q", cookie)
	}
	if key != "11223344-5566-7788-9900-AABBCCDDEEFF" {
		t.Errorf("key = %q", key)
	}
}

func parseCookie(out string) (cookie, key string, err error) {
	parts := []string{}
	for _, p := range []string{out} {
		parts = append(parts, splitWords(p)...)
	}
	if len(parts) < 2 {
		return "", "", fmtErr("unexpected cookie response: %q", out)
	}
	return parts[0], parts[1], nil
}

func splitWords(s string) []string {
	var words []string
	var cur string
	for _, c := range s {
		if c == ' ' || c == '\t' || c == '\n' {
			if cur != "" {
				words = append(words, cur)
				cur = ""
			}
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		words = append(words, cur)
	}
	return words
}

func fmtErr(format string, args ...any) error {
	return &testError{format: format, args: args}
}

type testError struct {
	format string
	args   []any
}

func (e *testError) Error() string {
	return "test error"
}
