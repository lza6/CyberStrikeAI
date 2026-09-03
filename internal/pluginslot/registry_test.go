package pluginslot

import "testing"

func TestRegisterAndGet(t *testing.T) {
	Reset()
	Register(SlotNotifier, "desktop", func(cfg map[string]interface{}) interface{} {
		return NewDesktopNotifier(cfg)
	}, nil)
	if got := Count(); got != 1 {
		t.Fatalf("Count = %d, want 1", got)
	}
	inst := Get(SlotNotifier, "desktop", nil)
	if inst == nil {
		t.Fatal("Get returned nil for registered desktop notifier")
	}
	if _, ok := inst.(*DesktopNotifier); !ok {
		t.Fatalf("Get returned %T, want *DesktopNotifier", inst)
	}
}

func TestGetMissingReturnsNil(t *testing.T) {
	Reset()
	if got := Get(SlotNotifier, "nonexistent", nil); got != nil {
		t.Fatalf("Get missing = %v, want nil", got)
	}
}

func TestListBySlot(t *testing.T) {
	Reset()
	Register(SlotNotifier, "desktop", func(cfg map[string]interface{}) interface{} {
		return NewDesktopNotifier(cfg)
	}, nil)
	Register(SlotNotifier, "webhook", func(cfg map[string]interface{}) interface{} {
		return NewWebhookNotifier(cfg)
	}, nil)
	Register(SlotTool, "modify-file", func(cfg map[string]interface{}) interface{} {
		return nil // placeholder
	}, nil)
	notifiers := List(SlotNotifier)
	if len(notifiers) != 2 {
		t.Fatalf("List(Notifier) = %d, want 2", len(notifiers))
	}
	tools := List(SlotTool)
	if len(tools) != 1 {
		t.Fatalf("List(Tool) = %d, want 1", len(tools))
	}
}

func TestRegisterWithManifest(t *testing.T) {
	Reset()
	RegisterWithManifest(Manifest{
		Name: "desktop-v2", Slot: SlotNotifier, Version: "2.0.0", DisplayName: "Desktop V2",
	}, func(cfg map[string]interface{}) interface{} {
		return NewDesktopNotifier(cfg)
	}, nil)
	list := List(SlotNotifier)
	if len(list) != 1 || list[0].Version != "2.0.0" || list[0].DisplayName != "Desktop V2" {
		t.Fatalf("RegisterWithManifest manifest mismatch: %+v", list)
	}
}

func TestDetectAvailable(t *testing.T) {
	Reset()
	// desktop：detect=nil → 视为可用
	Register(SlotNotifier, "desktop", func(cfg map[string]interface{}) interface{} {
		return NewDesktopNotifier(cfg)
	}, nil)
	// fake-unavailable：detect 返回 false
	Register(SlotNotifier, "fake-unavailable", func(cfg map[string]interface{}) interface{} {
		return nil
	}, func() bool { return false })
	avail := DetectAvailable(SlotNotifier)
	if len(avail) != 1 {
		t.Fatalf("DetectAvailable = %v, want [desktop]", avail)
	}
	if len(avail) == 1 && avail[0] != "desktop" {
		t.Fatalf("DetectAvailable[0] = %q, want desktop", avail[0])
	}
}

func TestRegisterDuplicateOverwrites(t *testing.T) {
	Reset()
	Register(SlotNotifier, "desktop", func(cfg map[string]interface{}) interface{} {
		return &DesktopNotifier{}
	}, nil)
	Register(SlotNotifier, "desktop", func(cfg map[string]interface{}) interface{} {
		return &WebhookNotifier{} // 故意换成不同类型
	}, nil)
	if got := Count(); got != 1 {
		t.Fatalf("duplicate Register should overwrite, Count = %d, want 1", got)
	}
	inst := Get(SlotNotifier, "desktop", nil)
	if _, ok := inst.(*WebhookNotifier); !ok {
		t.Fatalf("duplicate Register should overwrite with last factory, got %T", inst)
	}
}

func TestRegisterNilFactoryNoOp(t *testing.T) {
	Reset()
	Register(SlotNotifier, "noop", nil, nil)
	if got := Count(); got != 0 {
		t.Fatalf("nil factory should no-op, Count = %d, want 0", got)
	}
}

func TestWebhookNotifyEmptyURLNoOp(t *testing.T) {
	w := &WebhookNotifier{URL: ""}
	if err := w.Notify(NotifyEvent{Type: "test"}); err != nil {
		t.Fatalf("empty URL should no-op, got err %v", err)
	}
}

func TestNotifyEventFields(t *testing.T) {
	ev := NotifyEvent{
		ID:        "evt-1",
		Type:      "high-impact-tool",
		Priority:  "urgent",
		ProjectID: "proj-1",
		Message:   "test",
		Data:      map[string]interface{}{"tool": "nmap"},
	}
	if ev.Type != "high-impact-tool" || ev.Priority != "urgent" || ev.Data["tool"] != "nmap" {
		t.Fatalf("NotifyEvent fields mismatch: %+v", ev)
	}
}

func TestMakeKey(t *testing.T) {
	if got := makeKey(SlotNotifier, "desktop"); got != "notifier:desktop" {
		t.Fatalf("makeKey = %q, want notifier:desktop", got)
	}
}
