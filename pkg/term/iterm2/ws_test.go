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
				Session:        new("abc"),
				SplitDirection: &dir,
			},
		},
	}
	req.Id = new(int64(1))
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
				Session: new("session-1"),
				Text:    new("hello\n"),
			},
		},
	}
	req.Id = new(int64(2))
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
	req.Id = new(int64(4))
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
				Session:        new("active"),
				SplitDirection: &dir,
			},
		},
	}
	req.Id = new(int64(5))

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
