package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// applyEnv overrides config fields from environment variables, universally:
// every YAML field has a matching variable named CRMKIT_<PATH>, where PATH is
// the field's YAML path upper-cased with dots replaced by underscores. For
// example:
//
//	server.listen_addr   -> CRMKIT_SERVER_LISTEN_ADDR
//	storage.dsn          -> CRMKIT_STORAGE_DSN
//	ratelimit.rps        -> CRMKIT_RATELIMIT_RPS
//	email.smtp_pass      -> CRMKIT_EMAIL_SMTP_PASS
//	server.secret_key    -> CRMKIT_SERVER_SECRET_KEY
//
// Env vars take precedence over the config file. The mapping is derived from the
// struct's yaml tags, so new fields get an env var automatically.
func applyEnv(cfg *Config) error {
	return applyEnvStruct(reflect.ValueOf(cfg).Elem(), "CRMKIT")
}

func applyEnvStruct(v reflect.Value, prefix string) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		if comma := strings.IndexByte(tag, ','); comma >= 0 {
			tag = tag[:comma]
		}
		name := prefix + "_" + strings.ToUpper(tag)

		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			if err := applyEnvStruct(fv, name); err != nil {
				return err
			}
			continue
		}
		raw, ok := os.LookupEnv(name)
		if !ok {
			continue
		}
		if err := setScalar(fv, raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func setScalar(fv reflect.Value, raw string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return fmt.Errorf("expected an integer, got %q", raw)
		}
		fv.SetInt(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return fmt.Errorf("expected a number, got %q", raw)
		}
		fv.SetFloat(f)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("expected a boolean (true/false), got %q", raw)
		}
		fv.SetBool(b)
	default:
		return fmt.Errorf("unsupported field kind %s", fv.Kind())
	}
	return nil
}
