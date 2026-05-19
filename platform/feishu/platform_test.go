package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"

	"github.com/chenhg5/cc-connect/core"
	callback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestNew_DefaultsToInteractivePlatform(t *testing.T) {
	p, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := p.(core.CardSender); !ok {
		t.Fatal("expected default Feishu platform to implement core.CardSender")
	}
}

func TestNew_CanDisableInteractiveCards(t *testing.T) {
	p, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": false})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, ok := p.(core.CardSender); ok {
		t.Fatal("expected disabled Feishu platform to fall back to plain text")
	}
}

func TestNew_DisabledInteractiveCardsDoesNotStartPreviewCard(t *testing.T) {
	pAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": false})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	p, ok := pAny.(*Platform)
	if !ok {
		t.Fatalf("platform type = %T, want *Platform", pAny)
	}

	_, err = p.SendPreviewStart(context.Background(), replyContext{messageID: "om_x", chatID: "oc_x"}, "hello")
	if err == nil {
		t.Fatal("SendPreviewStart() error = nil, want not supported when cards are disabled")
	}
	if err != core.ErrNotSupported {
		t.Fatalf("SendPreviewStart() error = %v, want %v", err, core.ErrNotSupported)
	}
}

func TestNew_ProgressStyleDefaultLegacy(t *testing.T) {
	p, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	sp, ok := p.(core.ProgressStyleProvider)
	if !ok {
		t.Fatalf("platform type %T does not implement ProgressStyleProvider", p)
	}
	if got := sp.ProgressStyle(); got != "legacy" {
		t.Fatalf("ProgressStyle() = %q, want legacy", got)
	}
}

func TestNew_ProgressStyleSupportsCompactAndCard(t *testing.T) {
	tests := []string{"compact", "card"}
	for _, style := range tests {
		t.Run(style, func(t *testing.T) {
			p, err := New(map[string]any{
				"app_id":         "cli_xxx",
				"app_secret":     "secret",
				"progress_style": style,
			})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			sp, ok := p.(core.ProgressStyleProvider)
			if !ok {
				t.Fatalf("platform type %T does not implement ProgressStyleProvider", p)
			}
			if got := sp.ProgressStyle(); got != style {
				t.Fatalf("ProgressStyle() = %q, want %q", got, style)
			}
			payloadCap, ok := p.(core.ProgressCardPayloadSupport)
			if !ok {
				t.Fatalf("platform type %T does not implement ProgressCardPayloadSupport", p)
			}
			if !payloadCap.SupportsProgressCardPayload() {
				t.Fatal("SupportsProgressCardPayload() = false, want true")
			}
		})
	}
}

func TestNew_ProgressStyleRejectsInvalidValue(t *testing.T) {
	_, err := New(map[string]any{
		"app_id":         "cli_xxx",
		"app_secret":     "secret",
		"progress_style": "invalid-style",
	})
	if err == nil {
		t.Fatal("expected error for invalid progress_style")
	}
	if !strings.Contains(err.Error(), "invalid progress_style") {
		t.Fatalf("error = %q, want invalid progress_style", err.Error())
	}
}

func TestInteractivePlatform_OnMessagePassesCardSenderToHandler(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	messageID := "om_test_message"
	chatID := "oc_test_chat"
	openID := "ou_test_user"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"/help"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	var (
		wg           sync.WaitGroup
		receivedPlat core.Platform
		receivedMsg  *core.Message
	)
	wg.Add(1)
	ip.handler = func(p core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedPlat = p
		receivedMsg = msg
	}

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	wg.Wait()

	if receivedMsg == nil {
		t.Fatal("expected handler to receive a message")
	}
	if receivedMsg.Content != "/help" {
		t.Fatalf("message content = %q, want /help", receivedMsg.Content)
	}
	if _, ok := receivedPlat.(core.CardSender); !ok {
		t.Fatalf("handler platform type = %T, want core.CardSender", receivedPlat)
	}
}

func TestInteractivePlatform_CardActionPassesCardSenderToHandler(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	openID := "ou_test_user"
	chatID := "oc_test_chat"
	messageID := "om_test_message"
	action := "cmd:/help"

	var (
		msgCh  = make(chan *core.Message, 1)
		platCh = make(chan core.Platform, 1)
	)
	ip.handler = func(p core.Platform, msg *core.Message) {
		platCh <- p
		msgCh <- msg
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: openID},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": action}},
			Context:  &callback.Context{OpenChatID: chatID, OpenMessageID: messageID},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case receivedPlat := <-platCh:
		if _, ok := receivedPlat.(core.CardSender); !ok {
			t.Fatalf("handler platform type = %T, want core.CardSender", receivedPlat)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action handler invocation")
	}

	select {
	case receivedMsg := <-msgCh:
		if receivedMsg.Content != "/help" {
			t.Fatalf("message content = %q, want /help", receivedMsg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action message")
	}
}

func TestInteractivePlatform_CardActionTogglePassesThroughCardNavHandler(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	actionCh := make(chan string, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		actionCh <- action
		return core.NewCard().Markdown("ok").Build()
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/delete-mode toggle session-1"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("expected card update response for toggle, got %#v", resp)
	}
	if resp.Toast != nil {
		t.Fatalf("expected no toast-only shortcut for toggle, got %#v", resp.Toast)
	}

	select {
	case got := <-actionCh:
		if got != "act:/delete-mode toggle session-1" {
			t.Fatalf("action = %q, want act:/delete-mode toggle session-1", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card nav handler invocation")
	}
}

func TestInteractivePlatform_CardActionFormSubmitPassesSelectedIDs(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	actionCh := make(chan string, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		actionCh <- action
		return core.NewCard().Markdown("ok").Build()
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{
				Value: map[string]any{"action": "act:/delete-mode form-submit"},
				FormValue: map[string]any{
					deleteModeCheckerName("session-2"): true,
					deleteModeCheckerName("session-1"): true,
					deleteModeCheckerName("session-3"): false,
				},
			},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case got := <-actionCh:
		want := "act:/delete-mode form-submit session-1,session-2"
		if got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card nav handler invocation")
	}
}

func TestInteractivePlatform_CardActionFormSubmitUsesActionNameFallback(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	actionCh := make(chan string, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		actionCh <- action
		return core.NewCard().Markdown("ok").Build()
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{
				Name: "delete_mode_submit",
				FormValue: map[string]any{
					deleteModeCheckerName("session-2"): true,
					deleteModeCheckerName("session-1"): true,
				},
			},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case got := <-actionCh:
		want := "act:/delete-mode form-submit session-1,session-2"
		if got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card nav handler invocation")
	}
}

func TestInteractivePlatform_CardActionFormCancelUsesActionNameFallback(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	actionCh := make(chan string, 1)
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		actionCh <- action
		return core.NewCard().Markdown("ok").Build()
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{
				Name: "delete_mode_cancel",
			},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case got := <-actionCh:
		want := "act:/delete-mode cancel"
		if got != want {
			t.Fatalf("action = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card nav handler invocation")
	}
}

func TestInteractivePlatform_CardActionUsesCallbackSessionKey(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true, "thread_isolation": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	wantSessionKey := "feishu:oc_test_chat:root:om_root_thread"
	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":      "cmd:/help",
				"session_key": wantSessionKey,
			}},
			Context: &callback.Context{
				OpenChatID:    "oc_test_chat",
				OpenMessageID: "om_any_card_message",
			},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != wantSessionKey {
			t.Fatalf("SessionKey = %q, want %q", msg.SessionKey, wantSessionKey)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action message")
	}
}

func TestInteractivePlatform_CardActionSwitchCreatesTaskChat(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":                    "cli_xxx",
		"app_secret":                "secret",
		"enable_feishu_card":        true,
		"thread_isolation":          true,
		"session_switch_new_thread": true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	created := make(chan string, 1)
	ip.createTaskChatHook = func(_ context.Context, userID, title string) (string, error) {
		if userID != "ou_test_user" {
			t.Fatalf("userID = %q, want ou_test_user", userID)
		}
		created <- title
		return "oc_task_chat", nil
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":        "act:/switch 1",
				"action_mode":   "switch_session",
				"session_title": "chatgpt2api 分析当前部署",
			}},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_card_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("expected toast response, got %#v", resp)
	}

	select {
	case title := <-created:
		if title != "⏳[进行中]chatgpt2api" {
			t.Fatalf("task chat title = %q, want ⏳[进行中]chatgpt2api", title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected createTaskChatHook to be called")
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_task_chat" {
			t.Fatalf("SessionKey = %q, want feishu:oc_task_chat", msg.SessionKey)
		}
		if msg.ChannelKey != "oc_task_chat" {
			t.Fatalf("ChannelKey = %q, want oc_task_chat", msg.ChannelKey)
		}
		if msg.Content != "/switch 1" {
			t.Fatalf("Content = %q, want /switch 1", msg.Content)
		}
		rc, ok := msg.ReplyCtx.(replyContext)
		if !ok || !rc.taskChat || rc.chatID != "oc_task_chat" || rc.messageID != "" {
			t.Fatalf("ReplyCtx = %#v, want task chat context", msg.ReplyCtx)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action to dispatch a switch message")
	}
}

func TestInteractivePlatform_CardActionNewSessionCreatesTaskChat(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"thread_isolation":   true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	created := make(chan string, 1)
	ip.createTaskChatHook = func(_ context.Context, userID, title string) (string, error) {
		if userID != "ou_test_user" {
			t.Fatalf("userID = %q, want ou_test_user", userID)
		}
		created <- title
		return "oc_new_task_chat", nil
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":       "act:/new",
				"action_mode":  "thread_new_session",
				"thread_title": "Codex task",
			}},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_card_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("expected toast response, got %#v", resp)
	}

	select {
	case title := <-created:
		if title == "" {
			t.Fatalf("task chat title is empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected createTaskChatHook to be called")
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_new_task_chat" {
			t.Fatalf("SessionKey = %q, want feishu:oc_new_task_chat", msg.SessionKey)
		}
		if msg.Content != "/new" {
			t.Fatalf("Content = %q, want /new", msg.Content)
		}
		rc, ok := msg.ReplyCtx.(replyContext)
		if !ok || !rc.taskChat || rc.chatID != "oc_new_task_chat" || rc.messageID != "" {
			t.Fatalf("ReplyCtx = %#v, want task chat context", msg.ReplyCtx)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action to dispatch /new message")
	}
}

func TestInteractivePlatform_P2PNewCommandCreatesTaskChat(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"thread_isolation":   true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	created := make(chan string, 1)
	ip.createTaskChatHook = func(_ context.Context, userID, title string) (string, error) {
		if userID != "ou_test_user" {
			t.Fatalf("userID = %q, want ou_test_user", userID)
		}
		created <- title
		return "oc_auto_task_chat", nil
	}
	ip.sendPreviewStartHook = func(_ context.Context, rctx any, content string) (any, error) {
		rc, ok := rctx.(replyContext)
		if !ok {
			t.Fatalf("replyCtx type = %T", rctx)
		}
		if !rc.taskChat || rc.chatID != "oc_auto_task_chat" {
			t.Fatalf("preview replyCtx = %#v, want task chat oc_auto_task_chat", rc)
		}
		if content == "" {
			t.Fatalf("preview content = %q, want processing message", content)
		}
		return &feishuPreviewHandle{messageID: "om_processing", chatID: rc.chatID}, nil
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	messageID := "om_p2p_new"
	chatID := "ou_test_user"
	openID := "ou_test_user"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"/new jdk21涓殑铏氭嫙绾跨▼鏄粈涔堬紵涓庡師鏉ョ殑绾跨▼鏈夊暐鍖哄埆锛熸€庝箞鐢紵"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	if err := ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case title := <-created:
		if title == "" || !strings.Contains(title, "jdk21涓殑铏氭嫙绾跨▼") {
			t.Fatalf("task chat title = %q, want summary title", title)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected createTaskChatHook to be called")
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_auto_task_chat" {
			t.Fatalf("SessionKey = %q, want feishu:oc_auto_task_chat", msg.SessionKey)
		}
		if msg.ChannelKey != "oc_auto_task_chat" {
			t.Fatalf("ChannelKey = %q, want oc_auto_task_chat", msg.ChannelKey)
		}
		if msg.Content != "jdk21涓殑铏氭嫙绾跨▼鏄粈涔堬紵涓庡師鏉ョ殑绾跨▼鏈夊暐鍖哄埆锛熸€庝箞鐢紵" {
			t.Fatalf("Content = %q, want command body only", msg.Content)
		}
		rc, ok := msg.ReplyCtx.(replyContext)
		if !ok || !rc.taskChat || rc.chatID != "oc_auto_task_chat" || rc.messageID != "" {
			t.Fatalf("ReplyCtx = %#v, want task chat context", msg.ReplyCtx)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected p2p /new command to dispatch to task chat")
	}

	if handle := ip.popTaskChatPreviewHandle("oc_auto_task_chat"); handle == nil {
		t.Fatal("expected processing preview handle for new task chat")
	}
}

func TestInteractivePlatform_CardActionThreadSwitchCurrentCreatesTaskChat(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"thread_isolation":   true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)

	created := make(chan string, 1)
	ip.createTaskChatHook = func(_ context.Context, userID, title string) (string, error) {
		if userID != "ou_test_user" {
			t.Fatalf("userID = %q, want ou_test_user", userID)
		}
		created <- title
		return "oc_task_chat", nil
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":        "act:/switch 1",
				"action_mode":   "thread_switch_current",
				"session_title": "lazada task",
			}},
			Context: &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_card_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Toast == nil {
		t.Fatalf("expected toast response, got %#v", resp)
	}

	select {
	case title := <-created:
		if title == "" {
			t.Fatalf("task chat title is empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected createTaskChatHook to be called")
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_task_chat" {
			t.Fatalf("SessionKey = %q, want feishu:oc_task_chat", msg.SessionKey)
		}
		if msg.Content != "/switch 1" {
			t.Fatalf("Content = %q, want /switch 1", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action to dispatch /switch message")
	}
}

func TestInteractivePlatform_CardActionCurrentSessionRegistersTaskChatAlias(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"thread_isolation":   true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.createTaskChatHook = func(_ context.Context, userID, title string) (string, error) {
		return "oc_task_chat", nil
	}
	aliasCh := make(chan [2]string, 1)
	ip.sessionAliasHook = func(aliasSessionKey, targetSessionKey string) {
		aliasCh <- [2]string{aliasSessionKey, targetSessionKey}
	}
	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	_, err = ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action: &callback.CallBackAction{Value: map[string]any{
				"action":        "act:/current",
				"action_mode":   "thread_current_session",
				"session_title": "褰撳墠浼氳瘽",
				"session_key":   "feishu:oc_origin:ou_test_user",
			}},
			Context: &callback.Context{OpenChatID: "oc_origin", OpenMessageID: "om_card_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}

	select {
	case got := <-aliasCh:
		if got[0] != "feishu:oc_task_chat" || got[1] != "feishu:oc_origin:ou_test_user" {
			t.Fatalf("alias = %#v, want task chat alias to origin session", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected session alias to be registered")
	}
	select {
	case msg := <-msgCh:
		if msg.Content != "/current" {
			t.Fatalf("Content = %q, want /current", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected card action to dispatch /current message")
	}
}

func TestInteractivePlatform_DeleteSessionChatRemovesTaskGroup(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"thread_isolation":   true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.bindSessionTaskChat("session-123", "oc_task_chat")
	var gotChat string
	ip.deleteTaskChatHook = func(_ context.Context, chatID string) error {
		gotChat = chatID
		return nil
	}
	if err := ip.DeleteSessionChat(context.Background(), "session-123"); err != nil {
		t.Fatalf("DeleteSessionChat() error = %v", err)
	}
	if gotChat != "oc_task_chat" {
		t.Fatalf("chatID = %q, want oc_task_chat", gotChat)
	}
	if err := ip.DeleteSessionChat(context.Background(), "session-123"); err != nil {
		t.Fatalf("DeleteSessionChat() second call error = %v", err)
	}
}

func TestPlatform_FinalizeTaskChatPreviewUpdatesExistingCard(t *testing.T) {
	p := &Platform{
		platformName:           "feishu",
		useInteractiveCard:     true,
		taskChatPreviewHandles: map[string]*feishuPreviewHandle{},
	}
	var gotHandle any
	var gotContent string
	p.updateMessageHook = func(_ context.Context, previewHandle any, content string) error {
		gotHandle = previewHandle
		gotContent = content
		return nil
	}

	handle := &feishuPreviewHandle{messageID: "om_preview", chatID: "oc_task"}
	p.setTaskChatPreviewHandle("oc_task", handle)

	handled, err := p.FinalizeTaskChatPreview(context.Background(), replyContext{chatID: "oc_task", taskChat: true}, "澶勭悊瀹屾垚", core.CardStatusDone)
	if err != nil {
		t.Fatalf("FinalizeTaskChatPreview() error = %v", err)
	}
	if !handled {
		t.Fatal("FinalizeTaskChatPreview() handled = false, want true")
	}
	if gotHandle != handle {
		t.Fatalf("update handle = %#v, want %#v", gotHandle, handle)
	}
	if gotContent != "澶勭悊瀹屾垚" {
		t.Fatalf("update content = %q, want 澶勭悊瀹屾垚", gotContent)
	}
	if handle := p.popTaskChatPreviewHandle("oc_task"); handle != nil {
		t.Fatal("expected preview handle to be cleared after finalize")
	}
}

func TestBuildSwitchThreadTitleUsesSessionTitle(t *testing.T) {
	got := buildSwitchThreadTitle("2", "📌 chatgpt2api 分析服务")
	want := "Codex #2｜chatgpt2api 分析服务"
	if got != want {
		t.Fatalf("buildSwitchThreadTitle() = %q, want %q", got, want)
	}
}

func TestBuildSwitchThreadTitlePrefersExplicitThreadTitle(t *testing.T) {
	got := buildSwitchThreadTitle("2", map[string]any{
		"session_title": "📌 fallback",
		"thread_title":  "Codex #2｜clean title",
	})
	want := "Codex #2｜clean title"
	if got != want {
		t.Fatalf("buildSwitchThreadTitle() = %q, want %q", got, want)
	}
}

func TestBuildSwitchThreadTitleTruncatesLongTitle(t *testing.T) {
	got := buildSwitchThreadTitle("3", "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if !strings.HasPrefix(got, "Codex #3") {
		t.Fatalf("title prefix = %q, want switch thread prefix", got)
	}
	trimmed := strings.TrimPrefix(got, "Codex #3")
	if len([]rune(trimmed)) == 0 {
		t.Fatalf("truncated title is empty: %q", got)
	}
	if got == "" {
		t.Fatalf("title is empty")
	}
}

func TestBuildCommandThreadTitleNamesTopLevelList(t *testing.T) {
	got, ok := buildCommandThreadTitle("/list")
	if !ok {
		t.Fatal("buildCommandThreadTitle returned ok=false")
	}
	if got == "" {
		t.Fatalf("title is empty")
	}
}

func TestInteractivePlatform_TopLevelCommandDoesNotCreateNamedTopic(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"thread_isolation":   true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.botOpenID = "ou_bot"

	created := make(chan string, 1)
	ip.createThreadRootHook = func(_ context.Context, chatID, content string) (string, error) {
		created <- content
		return "om_named_root", nil
	}

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	msgID := "om_user_command"
	chatID := "oc_test_chat"
	userID := "ou_test_user"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"@_user_1 /list"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &userID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
				Mentions: []*larkim.MentionEvent{{
					Key:  stringPtr("@_user_1"),
					Name: stringPtr("ai_work"),
					Id:   &larkim.UserId{OpenId: stringPtr("ou_bot")},
				}},
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case title := <-created:
		t.Fatalf("unexpected topic creation: %q", title)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_test_chat:root:om_user_command" {
			t.Fatalf("SessionKey = %q, want original root session", msg.SessionKey)
		}
		if msg.Content != "/list" {
			t.Fatalf("Content = %q, want /list", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected command to dispatch without named topic")
	}
}

func TestInteractivePlatform_BotCreatedThreadAllowsNoMentionReplies(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":           "cli_xxx",
		"app_secret":       "secret",
		"thread_isolation": true,
		"group_reply_all":  false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.botOpenID = "ou_bot"
	ip.markBotThreadRoot("om_root")

	msgID := "om_reply"
	rootID := "om_root"
	chatID := "oc_test_chat"
	userID := "ou_test_user"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"continue without mention"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &userID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				RootId:      &rootID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_test_chat:root:om_root" {
			t.Fatalf("SessionKey = %q, want feishu:oc_test_chat:root:om_root", msg.SessionKey)
		}
		if msg.Content != "continue without mention" {
			t.Fatalf("Content = %q, want no-mention text", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected no-mention reply in bot-created thread to dispatch")
	}
}

func TestInteractivePlatform_BotCreatedTopicAllowsNoMentionRepliesByThreadID(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":           "cli_xxx",
		"app_secret":       "secret",
		"thread_isolation": true,
		"group_reply_all":  false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.botOpenID = "ou_bot"
	ip.markBotThreadID("omt_topic")

	msgID := "om_reply"
	rootID := "om_card_message"
	threadID := "omt_topic"
	parentID := "om_topic_root"
	chatID := "oc_test_chat"
	userID := "ou_test_user"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"continue in topic without mention"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &userID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				RootId:      &rootID,
				ParentId:    &parentID,
				ThreadId:    &threadID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_test_chat:root:om_card_message" {
			t.Fatalf("SessionKey = %q, want feishu:oc_test_chat:root:om_card_message", msg.SessionKey)
		}
		if msg.Content != "continue in topic without mention" {
			t.Fatalf("Content = %q, want no-mention topic text", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected no-mention reply in bot-created topic to dispatch")
	}
}

func TestInteractivePlatform_BotTopicNoMentionUsesCanonicalSessionAlias(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":           "cli_xxx",
		"app_secret":       "secret",
		"thread_isolation": true,
		"group_reply_all":  false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.botOpenID = "ou_bot"
	ip.markBotThreadIDForSession("omt_topic", "feishu:oc_test_chat:root:om_original_root")

	msgID := "om_reply"
	rootID := "om_feishu_topic_root"
	threadID := "omt_topic"
	parentID := "om_feishu_topic_root"
	chatID := "oc_test_chat"
	userID := "ou_test_user"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"continue in aliased topic"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &userID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				RootId:      &rootID,
				ParentId:    &parentID,
				ThreadId:    &threadID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.SessionKey != "feishu:oc_test_chat:root:om_original_root" {
			t.Fatalf("SessionKey = %q, want canonical root alias", msg.SessionKey)
		}
		if msg.Content != "continue in aliased topic" {
			t.Fatalf("Content = %q, want no-mention topic text", msg.Content)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected no-mention reply in aliased bot topic to dispatch")
	}
}

func TestInteractivePlatform_GroupMessageWithoutMentionOutsideBotThreadIgnored(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":           "cli_xxx",
		"app_secret":       "secret",
		"thread_isolation": true,
		"group_reply_all":  false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.botOpenID = "ou_bot"

	msgID := "om_regular_group_msg"
	chatID := "oc_test_chat"
	userID := "ou_test_user"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"do not wake bot"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &userID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		t.Fatalf("unexpected dispatch for regular no-mention group message: %#v", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestInteractivePlatform_MentionToOtherUserInsideBotThreadIgnored(t *testing.T) {
	platformAny, err := New(map[string]any{
		"app_id":           "cli_xxx",
		"app_secret":       "secret",
		"thread_isolation": true,
		"group_reply_all":  false,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.botOpenID = "ou_bot"
	ip.markBotThreadID("omt_topic")

	msgID := "om_reply_other_mention"
	rootID := "om_card_message"
	threadID := "omt_topic"
	parentID := "om_topic_root"
	chatID := "oc_test_chat"
	userID := "ou_test_user"
	otherID := "ou_other"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"@someone please check"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &userID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				RootId:      &rootID,
				ParentId:    &parentID,
				ThreadId:    &threadID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
				Mentions: []*larkim.MentionEvent{
					{
						Key:  stringPtr("@someone"),
						Name: stringPtr("someone"),
						Id:   &larkim.UserId{OpenId: &otherID},
					},
				},
			},
		},
	}

	if err := ip.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case msg := <-msgCh:
		t.Fatalf("unexpected dispatch for other-user mention inside bot thread: %#v", msg)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestFetchSingleMessageKeepsRawAppSenderID(t *testing.T) {
	p := &Platform{platformName: "feishu", appID: "cli_bot", client: lark.NewClient("cli_bot", "secret")}
	body := `{"code":0,"data":{"items":[{"msg_type":"text","parent_id":"","sender":{"id":"cli_bot","sender_type":"app"},"body":{"content":"{\"text\":\"topic root\"}"}}]}}`
	p.client = lark.NewClient("cli_bot", "secret", lark.WithOpenBaseUrl(newFeishuJSONServer(t, body)))

	msg := p.fetchSingleMessage(context.Background(), "om_root")
	if msg == nil {
		t.Fatal("fetchSingleMessage returned nil")
	}
	if msg.senderID != "cli_bot" {
		t.Fatalf("senderID = %q, want cli_bot", msg.senderID)
	}
	if msg.senderType != "app" {
		t.Fatalf("senderType = %q, want app", msg.senderType)
	}
}

func TestLearnBotThreadFromFetchedInteractiveMessageWithoutText(t *testing.T) {
	body := `{"code":0,"data":{"items":[{"msg_type":"interactive","thread_id":"omt_topic","sender":{"id":"cli_bot","sender_type":"app"},"body":{"content":""}}]}}`
	p := &Platform{
		platformName: "feishu",
		appID:        "cli_bot",
		client:       lark.NewClient("cli_bot", "secret", lark.WithOpenBaseUrl(newFeishuJSONServer(t, body))),
	}

	if !p.learnBotThreadFromFetchedMessage("om_root") {
		t.Fatal("learnBotThreadFromFetchedMessage returned false, want true for bot interactive card")
	}
	p.threadIsolation = true
	if !p.isBotThreadMessage(&larkim.EventMessage{
		MessageId: stringPtr("om_reply"),
		RootId:    stringPtr("om_root"),
		ThreadId:  stringPtr("omt_topic"),
		ParentId:  stringPtr("om_root"),
		ChatType:  stringPtr("group"),
	}) {
		t.Fatal("expected learned bot thread to allow no-mention replies")
	}
}

func TestFetchMessageMetaUsesFreshTenantTokenRetry(t *testing.T) {
	var mu sync.Mutex
	var msgRequests int
	var tokenRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/open-apis/im/v1/messages/om_root":
			mu.Lock()
			msgRequests++
			current := msgRequests
			mu.Unlock()
			if current == 1 {
				_, _ = w.Write([]byte(`{"code":99991663,"msg":"tenant access token invalid"}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"msg_type":"interactive","thread_id":"omt_topic","sender":{"id":"cli_bot","sender_type":"app"},"body":{"content":""}}]}}`))
		case r.URL.Path == "/open-apis/auth/v3/tenant_access_token/internal":
			mu.Lock()
			tokenRequests++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"fresh-token"}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := &Platform{
		platformName: "feishu",
		appID:        "cli_bot",
		appSecret:    "secret",
		client:       lark.NewClient("cli_bot", "secret", lark.WithOpenBaseUrl(srv.URL)),
		replayClient: lark.NewClient("cli_bot", "secret", lark.WithOpenBaseUrl(srv.URL)),
	}

	meta := p.fetchMessageMeta(context.Background(), "om_root")
	if meta == nil {
		t.Fatal("fetchMessageMeta returned nil")
	}
	if meta.threadID != "omt_topic" || meta.senderType != "app" || meta.senderID != "cli_bot" {
		t.Fatalf("meta = %#v, want bot app thread meta", meta)
	}
	mu.Lock()
	gotMsgRequests := msgRequests
	gotTokenRequests := tokenRequests
	mu.Unlock()
	if gotMsgRequests < 2 || gotTokenRequests < 1 {
		t.Fatalf("msgRequests=%d tokenRequests=%d, want fetch retry after token refresh", gotMsgRequests, gotTokenRequests)
	}
}

func TestInteractivePlatform_ModelCardActionReturnsCardUpdate(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	var gotAction, gotSessionKey string
	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		gotAction = action
		gotSessionKey = sessionKey
		return core.NewCard().Markdown("switching").Build()
	}

	resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
		Event: &callback.CardActionTriggerRequest{
			Operator: &callback.Operator{OpenID: "ou_test_user"},
			Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/model switch 1"}},
			Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
		},
	})
	if err != nil {
		t.Fatalf("onCardAction() error = %v", err)
	}
	if resp == nil || resp.Card == nil {
		t.Fatalf("expected card response, got %#v", resp)
	}
	if gotAction != "act:/model switch 1" {
		t.Fatalf("action = %q, want act:/model switch 1", gotAction)
	}
	if gotSessionKey == "" {
		t.Fatal("expected non-empty session key")
	}
	ip.cardActionMsgMu.Lock()
	tracked := ip.cardActionMsgIDs[gotSessionKey]
	ip.cardActionMsgMu.Unlock()
	if tracked != "om_test_message" {
		t.Fatalf("tracked message id = %q, want om_test_message", tracked)
	}
}

func TestInteractivePlatform_DeleteCardActionSlowReturnsToastThenRefreshes(t *testing.T) {
	platformAny, err := New(map[string]any{"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := platformAny.(*interactivePlatform)
	if !ok {
		t.Fatalf("platform type = %T, want *interactivePlatform", platformAny)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	refreshDone := make(chan struct{}, 1)
	var (
		refreshedSessionKey string
		refreshedCard       *core.Card
	)

	ip.cardNavHandler = func(action string, sessionKey string) *core.Card {
		if action != "act:/delete-one confirm 1" {
			t.Fatalf("action = %q, want act:/delete-one confirm 1", action)
		}
		started <- struct{}{}
		<-release
		return core.NewCard().Markdown("鍒犻櫎瀹屾垚").Build()
	}
	ip.updateMessageHook = func(ctx context.Context, previewHandle any, content string) error {
		return nil
	}
	ip.cardActionMsgMu.Lock()
	if ip.cardActionMsgIDs == nil {
		ip.cardActionMsgIDs = make(map[string]string)
	}
	ip.cardActionMsgMu.Unlock()
	ip.self = &stubRefreshPlatform{Platform: ip.Platform, refresh: func(ctx context.Context, sessionKey string, card *core.Card) error {
		refreshedSessionKey = sessionKey
		refreshedCard = card
		select {
		case refreshDone <- struct{}{}:
		default:
		}
		return nil
	}}

	respCh := make(chan *callback.CardActionTriggerResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := ip.onCardAction(&callback.CardActionTriggerEvent{
			Event: &callback.CardActionTriggerRequest{
				Operator: &callback.Operator{OpenID: "ou_test_user"},
				Action:   &callback.CallBackAction{Value: map[string]any{"action": "act:/delete-one confirm 1"}},
				Context:  &callback.Context{OpenChatID: "oc_test_chat", OpenMessageID: "om_test_message"},
			},
		})
		respCh <- resp
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cardNavHandler was not invoked")
	}

	select {
	case resp := <-respCh:
		err := <-errCh
		if err != nil {
			t.Fatalf("onCardAction() error = %v", err)
		}
		if resp == nil || resp.Toast == nil {
			t.Fatalf("expected toast response, got %#v", resp)
		}
		if !strings.Contains(resp.Toast.Content, "Loading") {
			t.Fatalf("toast content = %q, want loading hint", resp.Toast.Content)
		}
	case <-time.After(cardNavTimeout + time.Second):
		t.Fatal("expected timeout toast response")
	}

	close(release)

	select {
	case <-refreshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("expected async refresh after slow delete action")
	}

	if refreshedSessionKey == "" {
		t.Fatal("expected non-empty refreshed session key")
	}
	if refreshedCard == nil {
		t.Fatal("expected refreshed card")
	}
}

type stubRefreshPlatform struct {
	core.Platform
	refresh func(ctx context.Context, sessionKey string, card *core.Card) error
}

func (s *stubRefreshPlatform) RefreshCard(ctx context.Context, sessionKey string, card *core.Card) error {
	if s.refresh != nil {
		return s.refresh(ctx, sessionKey, card)
	}
	return nil
}

func TestNewLark_PlatformNameAndDomain(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret",
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	if p.Name() != "lark" {
		t.Fatalf("Name() = %q, want lark", p.Name())
	}
	ip, ok := p.(*interactivePlatform)
	if !ok {
		t.Fatalf("type = %T, want *interactivePlatform", p)
	}
	if ip.domain != lark.LarkBaseUrl {
		t.Fatalf("domain = %q, want %q", ip.domain, lark.LarkBaseUrl)
	}
}

func TestPlatformShouldUseWebhookMode(t *testing.T) {
	tests := []struct {
		name       string
		platform   string
		encryptKey string
		want       bool
	}{
		{name: "lark defaults to websocket", platform: "lark", want: false},
		{name: "lark webhook when encrypt key set", platform: "lark", encryptKey: "enc-key", want: true},
		{name: "feishu defaults to websocket", platform: "feishu", want: false},
		{name: "feishu webhook when encrypt key set", platform: "feishu", encryptKey: "enc-key", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Platform{platformName: tt.platform, encryptKey: tt.encryptKey}
			if got := p.shouldUseWebhookMode(); got != tt.want {
				t.Fatalf("shouldUseWebhookMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewFeishu_PlatformNameAndDomain(t *testing.T) {
	p, err := New(map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.Name() != "feishu" {
		t.Fatalf("Name() = %q, want feishu", p.Name())
	}
}

func TestNewFeishu_CustomDomainOverride(t *testing.T) {
	customDomain := "https://open.example.invalid"
	p, err := New(map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "domain": customDomain,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip, ok := p.(*interactivePlatform)
	if !ok {
		t.Fatalf("type = %T, want *interactivePlatform", p)
	}
	if ip.domain != customDomain {
		t.Fatalf("domain = %q, want %q", ip.domain, customDomain)
	}
}

func TestNewFeishu_InvalidCustomDomain(t *testing.T) {
	_, err := New(map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "domain": "://bad",
	})
	if err == nil {
		t.Fatal("expected invalid domain error")
	}
}

func TestLark_SessionKeyPrefix(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	ip := p.(*interactivePlatform)

	messageID := "om_test"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "p2p"
	senderType := "user"
	content := `{"text":"hello"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	var receivedMsg *core.Message
	var wg sync.WaitGroup
	wg.Add(1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedMsg = msg
	}

	_ = ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	})
	wg.Wait()

	if receivedMsg == nil {
		t.Fatal("handler not called")
	}
	if !strings.HasPrefix(receivedMsg.SessionKey, "lark:") {
		t.Fatalf("SessionKey = %q, want lark: prefix", receivedMsg.SessionKey)
	}
	if receivedMsg.Platform != "lark" {
		t.Fatalf("Platform = %q, want lark", receivedMsg.Platform)
	}
}

func TestLark_ThreadIsolationUsesRootSessionKey(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true, "thread_isolation": true,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	ip := p.(*interactivePlatform)

	messageID := "om_reply"
	rootID := "om_root"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"@bot hello"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	var receivedMsg *core.Message
	var wg sync.WaitGroup
	wg.Add(1)
	ip.botOpenID = "ou_bot"
	ip.handler = func(_ core.Platform, msg *core.Message) {
		defer wg.Done()
		receivedMsg = msg
	}

	_ = ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				RootId:      &rootID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
				Mentions: []*larkim.MentionEvent{
					{
						Key: stringPtr("@bot"),
						Id:  &larkim.UserId{OpenId: stringPtr("ou_bot")},
					},
				},
			},
		},
	})
	wg.Wait()

	if receivedMsg == nil {
		t.Fatal("handler not called")
	}
	if receivedMsg.SessionKey != "lark:oc_test:root:om_root" {
		t.Fatalf("SessionKey = %q, want lark:oc_test:root:om_root", receivedMsg.SessionKey)
	}
}

func TestLark_GroupReplyAllWithThreadIsolationUsesRootSessionKeyWithoutMention(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": true,
		"group_reply_all": true, "thread_isolation": true,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	ip := p.(*interactivePlatform)

	messageID := "om_root"
	chatID := "oc_test"
	openID := "ou_test"
	msgType := "text"
	chatType := "group"
	senderType := "user"
	content := `{"text":"hello from group root"}`
	createText := strconv.FormatInt(time.Now().UnixMilli(), 10)

	msgCh := make(chan *core.Message, 1)
	ip.handler = func(_ core.Platform, msg *core.Message) {
		msgCh <- msg
	}

	if err := ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Sender: &larkim.EventSender{
				SenderId:   &larkim.UserId{OpenId: &openID},
				SenderType: &senderType,
			},
			Message: &larkim.EventMessage{
				MessageId:   &messageID,
				ChatId:      &chatID,
				ChatType:    &chatType,
				MessageType: &msgType,
				Content:     &content,
				CreateTime:  &createText,
			},
		},
	}); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	select {
	case receivedMsg := <-msgCh:
		if receivedMsg.SessionKey != "lark:oc_test:root:om_root" {
			t.Fatalf("SessionKey = %q, want lark:oc_test:root:om_root", receivedMsg.SessionKey)
		}
		rc, ok := receivedMsg.ReplyCtx.(replyContext)
		if !ok {
			t.Fatalf("ReplyCtx type = %T, want replyContext", receivedMsg.ReplyCtx)
		}
		if rc.sessionKey != "lark:oc_test:root:om_root" {
			t.Fatalf("replyContext.sessionKey = %q, want lark:oc_test:root:om_root", rc.sessionKey)
		}
		if rc.messageID != "om_root" {
			t.Fatalf("replyContext.messageID = %q, want om_root", rc.messageID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected group root message to be handled without mention")
	}
}

func TestBuildReplyMessageReqBody_SetsReplyInThreadFlag(t *testing.T) {
	tests := []struct {
		name          string
		platform      *Platform
		replyCtx      replyContext
		wantThreading bool
	}{
		{
			name:          "thread isolation enabled",
			platform:      &Platform{threadIsolation: true},
			replyCtx:      replyContext{messageID: "om_reply", sessionKey: "feishu:oc_chat:root:om_root"},
			wantThreading: true,
		},
		{
			name:          "thread isolation does not affect p2p session",
			platform:      &Platform{threadIsolation: true},
			replyCtx:      replyContext{messageID: "om_reply", sessionKey: "feishu:oc_chat:ou_user"},
			wantThreading: false,
		},
		{
			name:          "plain reply remains non-threaded",
			platform:      &Platform{},
			replyCtx:      replyContext{messageID: "om_reply"},
			wantThreading: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.platform.buildReplyMessageReqBody(tt.replyCtx, larkim.MsgTypeText, `{"text":"hello"}`)
			if body == nil {
				t.Fatal("Body = nil, want populated reply body")
			}
			if body.ReplyInThread == nil {
				if tt.wantThreading {
					t.Fatal("ReplyInThread = nil, want true")
				}
				return
			}
			if got := *body.ReplyInThread; got != tt.wantThreading {
				t.Fatalf("ReplyInThread = %v, want %v", got, tt.wantThreading)
			}
		})
	}
}

func TestLark_ReconstructReplyCtx(t *testing.T) {
	p, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{
		"app_id": "cli_xxx", "app_secret": "secret", "enable_feishu_card": false,
	})
	if err != nil {
		t.Fatalf("newPlatform(lark) error = %v", err)
	}
	base := p.(*Platform)

	rctx, err := base.ReconstructReplyCtx("lark:oc_chat123:ou_user456")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx() error = %v", err)
	}
	rc := rctx.(replyContext)
	if rc.chatID != "oc_chat123" {
		t.Fatalf("chatID = %q, want oc_chat123", rc.chatID)
	}

	rctx, err = base.ReconstructReplyCtx("lark:oc_chat123:root:om_root456")
	if err != nil {
		t.Fatalf("ReconstructReplyCtx(thread) error = %v", err)
	}
	rc = rctx.(replyContext)
	if rc.chatID != "oc_chat123" {
		t.Fatalf("thread chatID = %q, want oc_chat123", rc.chatID)
	}
	if rc.messageID != "om_root456" {
		t.Fatalf("thread messageID = %q, want om_root456", rc.messageID)
	}

	_, err = base.ReconstructReplyCtx("feishu:oc_chat:ou_user")
	if err == nil {
		t.Fatal("expected error for feishu-prefixed key on lark platform")
	}
}

func TestUserIDFromEventFallsBackToUserID(t *testing.T) {
	userID := "uid_user123"
	if got := userIDFromEvent(&larkim.UserId{UserId: &userID}); got != userID {
		t.Fatalf("userIDFromEvent() = %q, want %q", got, userID)
	}
}

func TestResolveUserNameSkipsInvalidLookupID(t *testing.T) {
	p := &Platform{}
	for _, id := range []string{"", "feishu:oc_chat:ou_user", "ou user"} {
		if got := p.resolveUserName(id); got != id {
			t.Fatalf("resolveUserName(%q) = %q, want unchanged", id, got)
		}
	}
}

func stringPtr(s string) *string { return &s }

func newFeishuJSONServer(t *testing.T, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSanitizeMarkdownURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "http link kept",
			input: "see [docs](http://example.com)",
			want:  "see [docs](http://example.com)",
		},
		{
			name:  "https link kept",
			input: "see [docs](https://example.com/path)",
			want:  "see [docs](https://example.com/path)",
		},
		{
			name:  "file scheme removed",
			input: "open [file](file:///tmp/foo.txt)",
			want:  "open file (file:///tmp/foo.txt)",
		},
		{
			name:  "data scheme removed",
			input: "img [pic](data:image/png;base64,abc)",
			want:  "img pic (data:image/png;base64,abc)",
		},
		{
			name:  "mixed links",
			input: "[ok](https://x.com) and [bad](file:///etc/passwd)",
			want:  "[ok](https://x.com) and bad (file:///etc/passwd)",
		},
		{
			name:  "no links unchanged",
			input: "plain text without links",
			want:  "plain text without links",
		},
		{
			name:  "ftp scheme removed",
			input: "[dl](ftp://files.example.com/f.zip)",
			want:  "dl (ftp://files.example.com/f.zip)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeMarkdownURLs(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeMarkdownURLs(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestLark_ErrorMessagePrefix(t *testing.T) {
	_, err := newPlatform("lark", lark.LarkBaseUrl, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	if !strings.HasPrefix(err.Error(), "lark:") {
		t.Fatalf("error = %q, want lark: prefix", err.Error())
	}
}

func TestBuildPreviewCardJSON_ProgressPayloadUsesStructuredCard(t *testing.T) {
	payload := core.BuildProgressCardPayloadV2([]core.ProgressCardEntry{
		{Kind: core.ProgressEntryThinking, Text: "planning"},
		{Kind: core.ProgressEntryToolUse, Tool: "Bash", Text: "pwd"},
	}, false, "Codex", core.LangEnglish, core.ProgressCardStateRunning)
	if payload == "" {
		t.Fatal("BuildProgressCardPayload returned empty payload")
	}

	cardJSON := buildPreviewCardJSON(payload)
	if strings.Contains(cardJSON, core.ProgressCardPayloadPrefix) {
		t.Fatalf("card JSON should not leak payload prefix, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "Codex · Running") {
		t.Fatalf("card JSON should contain progress title, got %q", cardJSON)
	}
	if strings.Contains(cardJSON, "\"tag\":\"note\"") {
		t.Fatalf("card JSON should not use deprecated note tag, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "\"text_color\":\"grey\"") {
		t.Fatalf("card JSON should render thinking with grey style, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "\\u003ctext_tag color='blue'\\u003eTool") {
		t.Fatalf("card JSON should include tool label, got %q", cardJSON)
	}

	var card map[string]any
	if err := json.Unmarshal([]byte(cardJSON), &card); err != nil {
		t.Fatalf("card JSON is invalid: %v", err)
	}
	header, ok := card["header"].(map[string]any)
	if !ok || header == nil {
		t.Fatalf("expected header in card json, got %#v", card["header"])
	}
}

func TestBuildRichCard_RendersThinkingAndToolResultRows(t *testing.T) {
	code := 0
	success := true
	cardJSON := buildRichCard(core.CardStatusWorking, "", []core.ToolStep{
		{Kind: core.ToolStepKindThinking, Name: "Thinking", Summary: "Inspecting event routing"},
		{
			Kind:     core.ToolStepKindTool,
			Name:     "Bash",
			Summary:  "echo hi",
			Result:   "hi",
			Status:   "completed",
			ExitCode: &code,
			Success:  &success,
			Done:     true,
		},
	}, "done", true, time.Second)

	for _, want := range []string{"Inspecting event routing", "echo hi", "completed", "exit: 0", "hi"} {
		if !strings.Contains(cardJSON, want) {
			t.Fatalf("rich card should contain %q, got %q", want, cardJSON)
		}
	}
	if strings.Contains(cardJSON, core.ProgressCardPayloadPrefix) {
		t.Fatalf("rich card should not contain progress payload prefix, got %q", cardJSON)
	}
}

func TestBuildPreviewCardJSON_NormalTextFallback(t *testing.T) {
	cardJSON := buildPreviewCardJSON("plain progress text")
	if strings.Contains(cardJSON, "cc-connect 路 杩涘害") {
		t.Fatalf("normal text should use default card template, got %q", cardJSON)
	}
	if !strings.Contains(cardJSON, "\"tag\":\"markdown\"") {
		t.Fatalf("default preview card should contain markdown element, got %q", cardJSON)
	}
}

func TestFormatProgressToolInput_TodoWrite(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantContains    []string
		notWantContains []string
	}{
		{
			name: "valid todos with all statuses",
			input: `{"todos": [
				{"content": "Task 1", "status": "completed", "activeForm": "Completing task 1"},
				{"content": "Task 2", "status": "in_progress", "activeForm": "Working on task 2"},
				{"content": "Task 3", "status": "pending", "activeForm": "Planning task 3"}
			]}`,
			wantContains:    []string{"Task 1", "Task 2", "Task 3", "Completing task 1", "Working on task 2"},
			notWantContains: []string{"```"},
		},
		{
			name:            "todos without activeForm",
			input:           `{"todos": [{"content": "Simple task", "status": "pending"}]}`,
			wantContains:    []string{"Simple task"},
			notWantContains: []string{"(", ")"},
		},
		{
			name:         "invalid JSON falls back to default",
			input:        `not valid json`,
			wantContains: []string{"```text"},
		},
		{
			name:         "empty todos array",
			input:        `{"todos": []}`,
			wantContains: []string{"```text"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatProgressToolInput("TodoWrite", tt.input)
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("result should contain %q, got %q", want, result)
				}
			}
			for _, notWant := range tt.notWantContains {
				if strings.Contains(result, notWant) {
					t.Errorf("result should not contain %q, got %q", notWant, result)
				}
			}
		})
	}
}

func TestFormatProgressToolInput_OtherTools(t *testing.T) {
	// Non-TodoWrite tools should use default formatting
	result := formatProgressToolInput("Bash", "ls -la")
	if !strings.Contains(result, "```bash") {
		t.Errorf("Bash tool should use bash code block, got %q", result)
	}

	// TodoWrite with invalid JSON should fall back to text block
	result = formatProgressToolInput("TodoWrite", "not json")
	if !strings.Contains(result, "```text") {
		t.Errorf("TodoWrite with invalid JSON should fall back to text block, got %q", result)
	}
}

func TestAllowChat_FiltersGroupMessages(t *testing.T) {
	tests := []struct {
		name      string
		allowChat string
		chatID    string
		chatType  string
		wantPass  bool
	}{
		{"empty allow_chat permits all groups", "", "oc_abc", "group", true},
		{"wildcard permits all groups", "*", "oc_abc", "group", true},
		{"matching chat_id passes", "oc_abc", "oc_abc", "group", true},
		{"non-matching chat_id blocked", "oc_abc", "oc_xyz", "group", false},
		{"multiple chat_ids, match second", "oc_abc,oc_xyz", "oc_xyz", "group", true},
		{"multiple chat_ids, no match", "oc_abc,oc_def", "oc_xyz", "group", false},
		{"private chat bypasses allow_chat filter", "oc_abc", "oc_xyz", "p2p", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := newPlatform("feishu", lark.FeishuBaseUrl, map[string]any{
				"app_id": "cli_xxx", "app_secret": "secret",
				"enable_feishu_card": true,
				"group_reply_all":    true,
				"allow_chat":         tt.allowChat,
			})
			if err != nil {
				t.Fatalf("newPlatform() error = %v", err)
			}
			ip := p.(*interactivePlatform)

			messageID := "om_test_" + tt.name
			openID := "ou_test"
			msgType := "text"
			senderType := "user"
			content := `{"text":"hello"}`
			createTime := strconv.FormatInt(time.Now().UnixMilli(), 10)

			msgCh := make(chan *core.Message, 1)
			ip.handler = func(_ core.Platform, msg *core.Message) {
				msgCh <- msg
			}

			if err := ip.onMessage(context.Background(), &larkim.P2MessageReceiveV1{
				Event: &larkim.P2MessageReceiveV1Data{
					Sender: &larkim.EventSender{
						SenderId:   &larkim.UserId{OpenId: &openID},
						SenderType: &senderType,
					},
					Message: &larkim.EventMessage{
						MessageId:   &messageID,
						ChatId:      &tt.chatID,
						ChatType:    &tt.chatType,
						MessageType: &msgType,
						Content:     &content,
						CreateTime:  &createTime,
					},
				},
			}); err != nil {
				t.Fatalf("onMessage() error = %v", err)
			}

			select {
			case <-msgCh:
				if !tt.wantPass {
					t.Fatal("expected message to be blocked by allow_chat, but it was delivered")
				}
			case <-time.After(2 * time.Second):
				if tt.wantPass {
					t.Fatal("expected message to pass allow_chat filter, but it was blocked")
				}
			}
		})
	}
}

// --- Mention resolution tests ---

func TestResolveMentions_ReplacesKnownMember(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"zhangsan": "ou_zhangsan", "lisi": "ou_lisi"},
		fetchedAt: time.Now(),
	})
	input := "hello @zhangsan @lisi done"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, `<at user_id="ou_zhangsan">zhangsan</at>`) {
		t.Fatalf("expected zhangsan to be resolved, got %q", result)
	}
	if !strings.Contains(result, `<at user_id="ou_lisi">lisi</at>`) {
		t.Fatalf("expected lisi to be resolved, got %q", result)
	}
}

func TestResolveMentions_UnknownMemberKeptAsIs(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"zhangsan": "ou_zhangsan"},
		fetchedAt: time.Now(),
	})
	input := "@unknown done"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if strings.Contains(result, "<at") {
		t.Fatalf("unknown member should not be replaced, got %q", result)
	}
}

func TestUpdateConversationTopicPrefersTaskChatTitle(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	var gotChat string
	var gotTitle string
	p.updateTaskChatTitleHook = func(_ context.Context, chatID, title string) error {
		gotChat = chatID
		gotTitle = title
		return nil
	}

	err := p.UpdateConversationTopic(context.Background(), replyContext{
		chatID:     "oc_task_chat",
		sessionKey: "feishu:oc_task_chat:ou_test_user",
		taskChat:   true,
	}, "[进行中] lazada task")
	if err != nil {
		t.Fatalf("UpdateConversationTopic() error = %v", err)
	}
	if gotChat != "oc_task_chat" {
		t.Fatalf("chatID = %q, want oc_task_chat", gotChat)
	}
	if gotTitle != "⏳[进行中]lazada task" {
		t.Fatalf("title = %q, want running group title", gotTitle)
	}
}

func TestUpdateConversationTopicLegacyTopicStillWorks(t *testing.T) {
	p := &Platform{platformName: "feishu"}
	var gotRoot string
	var gotTitle string
	p.updateThreadRootHook = func(_ context.Context, rootID, content string) error {
		gotRoot = rootID
		gotTitle = content
		return nil
	}

	err := p.UpdateConversationTopic(context.Background(), replyContext{
		messageID:  "om_user_reply",
		chatID:     "oc_chat",
		sessionKey: "feishu:oc_chat:root:om_topic_root",
	}, "[进行中] topic task")
	if err != nil {
		t.Fatalf("UpdateConversationTopic() error = %v", err)
	}
	if gotRoot != "om_topic_root" {
		t.Fatalf("rootID = %q, want om_topic_root", gotRoot)
	}
	if gotTitle != "[进行中] topic task" {
		t.Fatalf("title = %q, want running title", gotTitle)
	}
}

func TestBuildActionThreadTitleIgnoresLegacyNewSessionTitle(t *testing.T) {
	got := buildActionThreadTitle("thread_new_session", "/new", map[string]any{"thread_title": "Codex锝滄柊浼氳瘽"})
	if got == "" {
		t.Fatalf("buildActionThreadTitle() returned empty title")
	}
}

func TestBuildActionTaskChatTitleUsesCompactStatusFormat(t *testing.T) {
	got := buildActionTaskChatTitle("thread_switch_session", "/switch 2", map[string]any{
		"session_name": "lazada一品多仓",
	})
	if got != "⏳[进行中]lazada一品多仓" {
		t.Fatalf("buildActionTaskChatTitle() = %q, want ⏳[进行中]lazada一品多仓", got)
	}
}

func TestTaskChatStorePersistsBotCreatedGroups(t *testing.T) {
	dir := t.TempDir()
	platformAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"cc_data_dir":        dir,
		"cc_project":         "trade_cloud-codex",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ip := platformAny.(*interactivePlatform)
	ip.markBotTaskChat("oc_task_chat")

	reloadedAny, err := New(map[string]any{
		"app_id":             "cli_xxx",
		"app_secret":         "secret",
		"enable_feishu_card": true,
		"cc_data_dir":        dir,
		"cc_project":         "trade_cloud-codex",
	})
	if err != nil {
		t.Fatalf("New() reload error = %v", err)
	}
	reloaded := reloadedAny.(*interactivePlatform)
	if !reloaded.isBotTaskChat("oc_task_chat") {
		t.Fatal("expected bot-created task chat marker to survive platform reload")
	}
}

func TestResolveMentions_LongestMatchFirst(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan", "张三丰": "ou_zhangsanfeng"},
		fetchedAt: time.Now(),
	})
	input := "@张三丰 请查看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, "ou_zhangsanfeng") {
		t.Fatalf("should match 张三丰 (longest), got %q", result)
	}
}

func TestResolveMentions_CardFormat(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan"},
		fetchedAt: time.Now(),
	})
	// Content with complex markdown triggers card format
	input := "# 巡检报告\n\n@张三 请查看\n\n```\nstatus: ok\n```"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, "<at id=ou_zhangsan></at>") {
		t.Fatalf("card format should use <at id=...>, got %q", result)
	}
}

func TestResolveMentions_DisabledByConfig(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: false}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "ou_zhangsan"},
		fetchedAt: time.Now(),
	})
	input := "@张三 请查看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if result != input {
		t.Fatalf("resolve_mentions=false should not replace, got %q", result)
	}
}

func TestResolveMentions_NoAtSign(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	input := "普通消息没有 at"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if result != input {
		t.Fatalf("no @ should return unchanged, got %q", result)
	}
}

func TestResolveMentions_DuplicateNameSkipped(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{"张三": "", "李四": "ou_lisi"},
		fetchedAt: time.Now(),
	})
	input := "请 @张三 和 @李四 看看"
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if !strings.Contains(result, "@张三") {
		t.Fatal("ambiguous name should be kept as-is")
	}
	if strings.Contains(result, "@李四") {
		t.Fatal("unique name should be resolved")
	}
}

func TestResolveMentions_SpecialCharsEscaped(t *testing.T) {
	p := &Platform{platformName: "feishu", resolveMentions: true}
	p.chatMemberCache.Store("oc_chat", &chatMemberEntry{
		members:   map[string]string{`A<"B">`: "ou_special"},
		fetchedAt: time.Now(),
	})
	input := `@A<"B"> 浣犲ソ`
	result := p.resolveMentionsInContent(context.Background(), "oc_chat", input)
	if strings.Contains(result, `<"B">`) {
		t.Fatalf("special chars should be escaped, got %q", result)
	}
	if !strings.Contains(result, "A&lt;") {
		t.Fatalf("expected HTML-escaped name, got %q", result)
	}
}
