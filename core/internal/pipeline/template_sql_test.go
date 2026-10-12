package pipeline

import (
	"errors"
	"testing"
)

type mapResolver map[string]string

func (m mapResolver) Resolve(ref string) (string, error) {
	if v, ok := m[ref]; ok {
		return v, nil
	}
	return "", errors.New("unknown " + ref)
}

func TestResolveTemplateSQL(t *testing.T) {
	r := mapResolver{"env:PW": "it's s3cret", "env:HOST": "db.local"}
	cases := []struct{ in, want string }{
		{`CREATE ROLE "a" LOGIN PASSWORD '{{env:PW}}';`, `CREATE ROLE "a" LOGIN PASSWORD 'it''s s3cret';`},
		{`CREATE SUBSCRIPTION s CONNECTION 'host={{env:HOST}} password={{env:PW}}' PUBLICATION p;`,
			`CREATE SUBSCRIPTION s CONNECTION 'host=db.local password=it''s s3cret' PUBLICATION p;`},
		{`COMMENT ON TABLE t IS 'it''s {{env:HOST}}';`, `COMMENT ON TABLE t IS 'it''s db.local';`},
		{`SELECT {{env:HOST}}`, `SELECT db.local`},
		{`SELECT 1;`, `SELECT 1;`},
		{`SELECT '{{not a ref'`, `SELECT '{{not a ref'`},
	}
	for _, c := range cases {
		got, err := ResolveTemplateSQL(c.in, r)
		if err != nil || got != c.want {
			t.Errorf("%s:\n got %q (%v)\nwant %q", c.in, got, err, c.want)
		}
	}
	if _, err := ResolveTemplateSQL(`'{{env:MISSING}}'`, r); err == nil {
		t.Error("an unknown reference must fail")
	}
}
