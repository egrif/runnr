package runbook

import "testing"

func TestBuiltinSensitive(t *testing.T) {
	m := NewMasker(nil)
	for in, want := range map[string]string{
		`password=hunter22`:                        `password=****`,
		`PGPASSWORD=hunter22 psql -h db`:           `PGPASSWORD=**** psql -h db`,
		`{"api_key": "abc123", "user": "app"}`:     `{"api_key": "****", "user": "app"}`,
		`client_secret: abc123`:                    `client_secret: ****`,
		`mysql --password hunter22 -h db`:          `mysql --password **** -h db`,
		`curl --token=abc123 x`:                    `curl --token=**** x`,
		`postgres://app:hunter22@db/x`:             `postgres://app:****@db/x`,
		`the tokenizer ran; 3 tokens; token is ok`: `the tokenizer ran; 3 tokens; token is ok`,
		`passed: 12, failed: 0`:                    `passed: 12, failed: 0`,
	} {
		if got := m.Mask(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
	for name, want := range map[string]bool{"db_password": true, "dbpass": true, "api_token": true, "admin_pw": true, "AWS_SECRET_ACCESS_KEY": true,
		"region": false, "passed": false, "host": false, "keyspace": false} {
		if SensitiveName(name) != want {
			t.Errorf("SensitiveName(%s) = %v", name, !want)
		}
	}
}
