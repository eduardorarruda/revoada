package config

import "testing"

func TestValidateSecrets(t *testing.T) {
	const strong = "um-segredo-bem-forte-com-mais-de-32-bytes!!"
	cases := []struct {
		name    string
		env     string
		secret  string
		wantErr bool
	}{
		{"prod sem secret aborta", "production", "", true},
		{"env vazio + default aborta", "", "", true},
		{"env desconhecido + default aborta", "staging", "", true},
		{"dev + default ok", "development", "", false},
		{"dev abreviado + default ok", "dev", "", false},
		{"local + default ok", "local", "", false},
		{"test + default ok", "test", "", false},
		{"dev case-insensitive ok", "DEV", "", false},
		{"prod + secret forte ok", "production", strong, false},
		{"env vazio + secret forte ok", "", strong, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("REVOADA_ENV", c.env)
			t.Setenv("REVOADA_JWT_SECRET", c.secret)
			err := ValidateSecrets()
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateSecrets() err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func TestRetentionDays(t *testing.T) {
	cases := []struct {
		env       string
		wantDays  int
		wantValid bool
	}{
		{"", 120, true},
		{"30", 30, true},
		{"0", 120, false},
		{"-5", 120, false},
		{"abc", 120, false},
		{"365", 365, true},
	}
	for _, c := range cases {
		t.Run("env="+c.env, func(t *testing.T) {
			t.Setenv("REVOADA_RETENTION_DAYS", c.env)
			days, valid := RetentionDays()
			if days != c.wantDays || valid != c.wantValid {
				t.Fatalf("RetentionDays()=(%d,%v), want (%d,%v)", days, valid, c.wantDays, c.wantValid)
			}
		})
	}
}

func TestPGMaxConns(t *testing.T) {
	cases := []struct {
		env  string
		want int32
	}{
		{"", 20},
		{"50", 50},
		{"0", 20},
		{"-1", 20},
		{"xyz", 20},
	}
	for _, c := range cases {
		t.Run("env="+c.env, func(t *testing.T) {
			t.Setenv("REVOADA_PG_MAX_CONNS", c.env)
			if got := PGMaxConns(); got != c.want {
				t.Fatalf("PGMaxConns()=%d, want %d", got, c.want)
			}
		})
	}
}
