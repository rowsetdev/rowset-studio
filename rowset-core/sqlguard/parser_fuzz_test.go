package sqlguard

import "testing"

func FuzzParseNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"select * from users where tenant_id = 7",
		"with x as (select id from users) select * from x",
		"update users set active=false where id=1",
		"select 'unterminated",
		"select /* nested /* comment */ still */ 1",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		for _, dialect := range []Dialect{DialectGeneric, DialectPostgres, DialectMySQL, DialectMSSQL} {
			info, _ := ParseDialect(dialect, sql)
			if info.Dialect != dialect {
				t.Fatalf("dialect lost: got %q, want %q", info.Dialect, dialect)
			}
		}
	})
}

func BenchmarkParseSecurityIR(b *testing.B) {
	queries := []string{
		"select id, email from public.customers where tenant_id = 7 order by created_at desc limit 100",
		"with recent as (select id from customers where tenant_id=7) select r.id, o.total from recent r left join orders o on o.customer_id=r.id",
		"update customers set active=false where tenant_id=7 and last_seen < now() - interval '1 year'",
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ParseDialect(DialectPostgres, queries[i%len(queries)]); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseSecurityIRCold(b *testing.B) {
	query := "with recent as (select id from customers where tenant_id=7) select r.id, o.total from recent r left join orders o on o.customer_id=r.id"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := parseDialect(DialectPostgres, query); err != nil {
			b.Fatal(err)
		}
	}
}
