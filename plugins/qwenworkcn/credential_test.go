package main

import "testing"

// 账号 JSON 里的组织标签是 JSON 数组, 按 []string 断言会永远拿到空值, 企业账号的身份字段会静默丢失。
func TestParseCredentialReadsOrganizationTags(t *testing.T) {
	cred, err := parseCredential([]byte(`{
		"userId": "u1",
		"displayName": "tester",
		"endpoint": "https://example.com",
		"machineId": "m1",
		"credentials": {"access": "tok", "refresh": "ref"},
		"organization_id": "org-1",
		"organization_tags": ["tag-a", "tag-b"]
	}`))
	if err != nil {
		t.Fatalf("parseCredential: %v", err)
	}

	tags := stringList(cred.extra["organization_tags"])
	if len(tags) != 2 || tags[0] != "tag-a" || tags[1] != "tag-b" {
		t.Fatalf("organization_tags = %v, want [tag-a tag-b]", tags)
	}
	if orgID, _ := cred.extra["organization_id"].(string); orgID != "org-1" {
		t.Fatalf("organization_id = %q, want org-1", orgID)
	}
}
