package cloudcodec

func (u Usage) Plus(v Usage, only bool) Usage {
	for _, f := range []struct{ a, b *int }{{&u.Input, &v.Input}, {&u.Output, &v.Output}, {&u.CacheRead, &v.CacheRead}, {&u.CacheWrite, &v.CacheWrite}, {&u.CacheWrite1h, &v.CacheWrite1h}, {&u.Reasoning, &v.Reasoning}} {
		if *f.a > 0 || !only {
			*f.a += *f.b
		}
	}
	return u
}
