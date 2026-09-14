package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
)

var nowFunc = time.Now

const (
	CosyB64Alphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	CosyB64Pad      = "$"
	CosyChatPath    = "/api/v2/service/pro/sse/agent_chat_generation"
	CosyRSA1024PEM  = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`
)

var (
	cosyPublicKey *rsa.PublicKey
	cosyB64Map    [256]int
)

func init() {
	// 初始化自定义 base64 逆向映射表
	for i := range cosyB64Map {
		cosyB64Map[i] = -1
	}
	for i := 0; i < len(CosyB64Alphabet); i++ {
		cosyB64Map[CosyB64Alphabet[i]] = i
	}

	// 解析内嵌 RSA-1024 公钥
	block, _ := pem.Decode([]byte(CosyRSA1024PEM))
	if block == nil {
		panic("failed to decode Cosy RSA PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(fmt.Sprintf("failed to parse Cosy public key: %v", err))
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		panic("Cosy public key is not RSA")
	}
	cosyPublicKey = rsaPub
}

func endSwapK(n int) int {
	return 2*n - (2*n+2)/3
}

func endSwap(raw string, k int) string {
	l := len(raw)
	if k == 0 || l == 0 {
		return raw
	}
	runes := []byte(raw)
	for i := 0; i < k; i++ {
		runes[i] = raw[l-k+i]
		runes[l-k+i] = raw[i]
	}
	return string(runes)
}

func standardCustomB64(bytes []byte) string {
	var sb strings.Builder
	l := len(bytes)
	sb.Grow(((l + 2) / 3) * 4)

	for i := 0; i < l; i += 3 {
		n := l - i
		if n > 3 {
			n = 3
		}
		b0 := bytes[i]
		var b1, b2 byte
		if n > 1 {
			b1 = bytes[i+1]
		}
		if n > 2 {
			b2 = bytes[i+2]
		}

		s0 := (b0 >> 2) & 63
		s1 := ((b0 & 3) << 4) | ((b1 >> 4) & 15)
		s2 := ((b1 & 15) << 2) | ((b2 >> 6) & 3)
		s3 := b2 & 63

		sb.WriteByte(CosyB64Alphabet[s0])
		sb.WriteByte(CosyB64Alphabet[s1])
		if n == 1 {
			sb.WriteString(CosyB64Pad)
			sb.WriteString(CosyB64Pad)
		} else if n == 2 {
			sb.WriteByte(CosyB64Alphabet[s2])
			sb.WriteString(CosyB64Pad)
		} else {
			sb.WriteByte(CosyB64Alphabet[s2])
			sb.WriteByte(CosyB64Alphabet[s3])
		}
	}
	return sb.String()
}

func customB64Decode(raw string) ([]byte, error) {
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("custom b64 length %d not multiple of 4", len(raw))
	}
	out := make([]byte, 0, (len(raw)/4)*3)
	for i := 0; i < len(raw); i += 4 {
		c0 := raw[i]
		c1 := raw[i+1]
		c2 := raw[i+2]
		c3 := raw[i+3]

		v0 := cosyB64Map[c0]
		v1 := cosyB64Map[c1]
		if v0 < 0 || v1 < 0 {
			return nil, fmt.Errorf("invalid cosy b64 char at %d", i)
		}

		pad2 := c2 == CosyB64Pad[0]
		pad3 := c3 == CosyB64Pad[0]

		var v2, v3 int
		if pad2 {
			v2 = 0
		} else {
			v2 = cosyB64Map[c2]
			if v2 < 0 {
				return nil, fmt.Errorf("invalid cosy b64 char at %d", i+2)
			}
		}

		if pad3 {
			v3 = 0
		} else {
			v3 = cosyB64Map[c3]
			if v3 < 0 {
				return nil, fmt.Errorf("invalid cosy b64 char at %d", i+3)
			}
		}

		out = append(out, byte((v0<<2)|(v1>>4)))
		if !pad2 {
			out = append(out, byte(((v1&15)<<4)|(v2>>2)))
		}
		if !pad3 {
			out = append(out, byte(((v2&3)<<6)|v3))
		}
	}
	return out, nil
}

func encodeCosyBody(plain string) (string, error) {
	raw := standardCustomB64([]byte(plain))
	if len(raw)%4 != 0 {
		return "", fmt.Errorf("cosy body raw length %d not multiple of 4", len(raw))
	}
	n := len(raw) / 4
	return endSwap(raw, endSwapK(n)), nil
}

func decodeCosyBody(encoded string) (string, error) {
	l := len(encoded)
	if l%4 != 0 {
		return "", fmt.Errorf("cosy body encoded length %d not multiple of 4", l)
	}
	n := l / 4
	raw := endSwap(encoded, endSwapK(n))
	decoded, err := customB64Decode(raw)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func uuidBytesFromRaw(raw16 []byte) []byte {
	b := make([]byte, 16)
	for i := 0; i < 16; i++ {
		b[i] = raw16[15-i]
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return b
}

func sessionKeyAscii(raw16 []byte) []byte {
	u := uuidBytesFromRaw(raw16)
	dst := make([]byte, 16)
	hex.Encode(dst, u[:8])
	return dst
}

func pkcs7Pad(data []byte) []byte {
	n := 16 - (len(data) & 15)
	padding := bytes.Repeat([]byte{byte(n)}, n)
	return append(data, padding...)
}

func pkcs1v15Encrypt(msg16 []byte, ps109 []byte) ([]byte, error) {
	if len(msg16) != 16 {
		return nil, fmt.Errorf("RSA msg must be 16, got %d", len(msg16))
	}
	if len(ps109) != 109 {
		return nil, fmt.Errorf("PS must be 109, got %d", len(ps109))
	}
	em := make([]byte, 128)
	em[0] = 0x00
	em[1] = 0x02
	copy(em[2:111], ps109)
	em[111] = 0x00
	copy(em[112:128], msg16)

	m := new(big.Int).SetBytes(em)
	e := big.NewInt(int64(cosyPublicKey.E))
	c := new(big.Int).Exp(m, e, cosyPublicKey.N)

	cBytes := c.Bytes()
	out := make([]byte, 128)
	copy(out[128-len(cBytes):], cBytes)
	return out, nil
}

func encryptUserInfo(raw16 []byte, payloadJSON string) ([]byte, error) {
	sk := sessionKeyAscii(raw16)
	block, err := aes.NewCipher(sk)
	if err != nil {
		return nil, fmt.Errorf("create aes cipher: %w", err)
	}

	mode := cipher.NewCBCEncrypter(block, sk)
	padded := pkcs7Pad([]byte(payloadJSON))
	ciphertext := make([]byte, len(padded))
	mode.CryptBlocks(ciphertext, padded)
	return ciphertext, nil
}

func randomNonZeroPs109() ([]byte, error) {
	ps := make([]byte, 109)
	for i := 0; i < 109; i++ {
		var b [1]byte
		for b[0] == 0 {
			if _, err := rand.Read(b[:]); err != nil {
				return nil, err
			}
		}
		ps[i] = b[0]
	}
	return ps, nil
}

type runtimeResult struct {
	Key             string
	EncryptUserInfo string
	SessionKeyAscii string
}

func runtimeFromRaw(raw16 []byte, ps109 []byte, payloadJSON string) (*runtimeResult, error) {
	if len(raw16) != 16 {
		return nil, fmt.Errorf("raw16 must be 16, got %d", len(raw16))
	}
	ps := make([]byte, len(ps109))
	copy(ps, ps109)
	for i := range ps {
		if ps[i] == 0 {
			ps[i] = 1
		}
	}
	sk := sessionKeyAscii(raw16)

	encKey, err := pkcs1v15Encrypt(sk, ps)
	if err != nil {
		return nil, err
	}

	encUser, err := encryptUserInfo(raw16, payloadJSON)
	if err != nil {
		return nil, err
	}

	return &runtimeResult{
		Key:             base64.StdEncoding.EncodeToString(encKey),
		EncryptUserInfo: base64.StdEncoding.EncodeToString(encUser),
		SessionKeyAscii: string(sk),
	}, nil
}

func generateRuntimeAuthFields(payloadJSON string) (*runtimeResult, error) {
	var raw16 [16]byte
	if _, err := rand.Read(raw16[:]); err != nil {
		return nil, fmt.Errorf("read random raw16: %w", err)
	}
	ps109, err := randomNonZeroPs109()
	if err != nil {
		return nil, fmt.Errorf("generate random ps109: %w", err)
	}
	return runtimeFromRaw(raw16[:], ps109, payloadJSON)
}

type runtimePayload struct {
	UID              string   `json:"uid"`
	OrganizationID   string   `json:"organization_id"`
	OrganizationTags []string `json:"organization_tags"`
	DataPolicyAgreed bool     `json:"data_policy_agreed"`
}

type cosyP1Payload struct {
	Version     string `json:"version"`
	RequestID   string `json:"requestId"`
	Info        string `json:"info"`
	CosyVersion string `json:"cosyVersion"`
	IDEVersion  string `json:"ideVersion"`
}

func encodeCosyPart2(p1, cosyKey, cosyDate, bodyEncoded, path string) string {
	msg := p1 + "\n" + cosyKey + "\n" + cosyDate + "\n" + bodyEncoded + "\n" + path
	sum := md5.Sum([]byte(msg))
	return hex.EncodeToString(sum[:])
}

func buildCosyAuthorization(p1, cosyKey, cosyDate, bodyEncoded, path string) string {
	part2 := encodeCosyPart2(p1, cosyKey, cosyDate, bodyEncoded, path)
	return "Bearer COSY." + p1 + "." + part2
}

type SignInferUser struct {
	UID              string
	OrganizationID   string
	OrganizationTags []string
	DataPolicyAgreed *bool
}

type PreparedInferRequest struct {
	URL     string
	Headers map[string]string
	Body    string
}

type FixedRuntime struct {
	Raw16 []byte
	PS109 []byte
}

type SignInferInput struct {
	Endpoint      string
	MachineID     string
	DeviceToken   string
	Body          string
	User          *SignInferUser
	CosyVersion   string
	StaticHeaders map[string]string

	// 测试注入用
	RequestID    string
	CosyDate     string
	FixedRuntime *FixedRuntime
}

func signCosyRequest(input SignInferInput) (*PreparedInferRequest, error) {
	if strings.TrimSpace(input.DeviceToken) == "" {
		return nil, errors.New("sign: deviceToken 为空")
	}
	if strings.TrimSpace(input.MachineID) == "" {
		return nil, errors.New("sign: machineId 为空")
	}
	if strings.TrimSpace(input.CosyVersion) == "" {
		return nil, errors.New("sign: cosyVersion 为空")
	}
	if input.StaticHeaders == nil {
		return nil, errors.New("sign: staticHeaders 为空")
	}
	if _, ok := input.StaticHeaders["Cosy-Version"]; !ok {
		return nil, errors.New("sign: staticHeaders 缺少 Cosy-Version")
	}
	dataPolicy, ok := input.StaticHeaders["Cosy-Data-Policy"]
	if !ok {
		return nil, errors.New("sign: staticHeaders 缺少 Cosy-Data-Policy")
	}

	user := input.User
	if user == nil {
		user = &SignInferUser{UID: ""}
	}

	dataPolicyAgreed := true
	if strings.EqualFold(strings.TrimSpace(dataPolicy), "disagree") {
		dataPolicyAgreed = false
	}
	if user.DataPolicyAgreed != nil {
		dataPolicyAgreed = *user.DataPolicyAgreed
	}

	orgTags := user.OrganizationTags
	if orgTags == nil {
		orgTags = []string{}
	}

	payload := runtimePayload{
		UID:              user.UID,
		OrganizationID:   user.OrganizationID,
		OrganizationTags: orgTags,
		DataPolicyAgreed: dataPolicyAgreed,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal runtime payload: %w", err)
	}

	var runtime *runtimeResult
	if input.FixedRuntime != nil {
		runtime, err = runtimeFromRaw(input.FixedRuntime.Raw16, input.FixedRuntime.PS109, string(payloadBytes))
	} else {
		runtime, err = generateRuntimeAuthFields(string(payloadBytes))
	}
	if err != nil {
		return nil, fmt.Errorf("generate runtime fields: %w", err)
	}

	requestID := input.RequestID
	if requestID == "" {
		requestID = uuid.NewString()
	}

	cosyDate := input.CosyDate
	if cosyDate == "" {
		cosyDate = fmt.Sprintf("%d", currentTimestampSeconds())
	}

	bodyEncoded, err := encodeCosyBody(input.Body)
	if err != nil {
		return nil, fmt.Errorf("encode cosy body: %w", err)
	}

	p1Struct := cosyP1Payload{
		Version:     "v1",
		RequestID:   requestID,
		Info:        runtime.EncryptUserInfo,
		CosyVersion: input.CosyVersion,
		IDEVersion:  "",
	}
	p1Bytes, err := json.Marshal(p1Struct)
	if err != nil {
		return nil, fmt.Errorf("marshal cosy p1: %w", err)
	}
	p1 := base64.StdEncoding.EncodeToString(p1Bytes)

	authHeader := buildCosyAuthorization(p1, runtime.Key, cosyDate, bodyEncoded, CosyChatPath)

	headers := make(map[string]string, len(input.StaticHeaders)+6)
	for k, v := range input.StaticHeaders {
		headers[k] = v
	}
	headers["Authorization"] = authHeader
	headers["Cosy-Date"] = cosyDate
	headers["Cosy-Key"] = runtime.Key
	headers["Cosy-MachineId"] = input.MachineID
	headers["Cosy-MachineToken"] = input.DeviceToken
	headers["Cosy-User"] = user.UID

	base := strings.TrimRight(input.Endpoint, "/")
	chatURL := fmt.Sprintf("%s/algo%s?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1", base, CosyChatPath)

	return &PreparedInferRequest{
		URL:     chatURL,
		Headers: headers,
		Body:    bodyEncoded,
	}, nil
}

func currentTimestampSeconds() int64 {
	return nowFunc().Unix()
}
