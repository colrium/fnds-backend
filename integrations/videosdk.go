package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"time"
)

type VideoSDKService struct {
	apiKey    string
	apiSecret string
	baseURL   string
}

func NewVideoSDKService() *VideoSDKService {
	return &VideoSDKService{
		apiKey:    os.Getenv("VIDEOSDK_API_KEY"),
		apiSecret: os.Getenv("VIDEOSDK_SECRET_KEY"),
		baseURL:   os.Getenv("VIDEOSDK_BASE_URL"),
	}
}

func (s *VideoSDKService) CreateJwtToken() string {
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerBytes, _ := json.Marshal(header)

	payload := map[string]interface{}{
		"apikey":      s.apiKey,
		"permissions": []string{"allow_join"},
		"roles":       []string{"crawler", "rtc"},
		"iat":         time.Now().Unix(),
		"exp":         time.Now().Add(2 * time.Hour).Unix(),
	}
	payloadBytes, _ := json.Marshal(payload)

	headerEnc := hex.EncodeToString(headerBytes)
	payloadEnc := hex.EncodeToString(payloadBytes)

	mac := hmac.New(sha256.New, []byte(s.apiSecret))
	mac.Write([]byte(headerEnc + "." + payloadEnc))
	signature := hex.EncodeToString(mac.Sum(nil))

	return headerEnc + "." + payloadEnc + "." + signature
}
