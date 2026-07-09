package chat

import "testing"

func TestValidateCreateChat(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		chatType string
		wantErr  bool
		errMsg   string
	}{
		{"valid message", "Hello, I'll be there at 10am", "message", false, ""},
		{"valid schedule", "Meeting at 2pm", "schedule", false, ""},
		{"valid address", "123 Main St", "address", false, ""},
		{"empty message", "", "message", true, "message is required"},
		{"oversized message", string(make([]byte, 256)), "message", true, "message must not exceed 255 characters"},
		{"invalid chat type", "Hello", "invalid", true, "chat_type must be one of message, schedule, address"},
		{"empty chat type", "Hello", "", true, "chat_type must be one of message, schedule, address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCreateChat(tt.message, tt.chatType)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				} else if err.Error() != tt.errMsg {
					t.Errorf("expected error %q, got %q", tt.errMsg, err.Error())
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
