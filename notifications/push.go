package notifications

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

type PushNotificationRequest struct {
	Token string            `json:"token"`
	Title string            `json:"title"`
	Body  string            `json:"body"`
	Data  map[string]string `json:"data,omitempty"`
}

type FCMService struct {
	ServerKey string
}

func NewFCMService(serverKey string) *FCMService {
	return &FCMService{ServerKey: serverKey}
}

type fcmMessage struct {
	To           string            `json:"to"`
	Notification fcmNotification   `json:"notification,omitempty"`
	Data         map[string]string `json:"data,omitempty"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (s *FCMService) SendPush(req PushNotificationRequest) error {
	msg := fcmMessage{
		To: req.Token,
		Notification: fcmNotification{
			Title: req.Title,
			Body:  req.Body,
		},
		Data: req.Data,
	}

	body := new(bytes.Buffer)
	json.NewEncoder(body).Encode(msg)

	httpReq, err := http.NewRequest("POST", "https://fcm.googleapis.com/fcm/send", body)
	if err != nil {
		return fmt.Errorf("failed to create FCM request: %w", err)
	}

	httpReq.Header.Set("Authorization", "key="+s.ServerKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send FCM request: %w", err)
	}
	defer resp.Body.Close()

	log.Printf("[FCMService] push sent to %s, status: %d", req.Token, resp.StatusCode)
	return nil
}

func SendPushNotification(serverKey, token, title, body string) error {
	service := NewFCMService(serverKey)
	return service.SendPush(PushNotificationRequest{
		Token: token,
		Title: title,
		Body:  body,
	})
}
