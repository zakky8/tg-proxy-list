package model

import "testing"

func TestFakeTLSDomain(t *testing.T) {
	// ee + 16 bytes (32 hex) + hex("example.com")
	secret := "ee" + "0123456789abcdef0123456789abcdef" + "6578616d706c652e636f6d"
	p := Proxy{Server: "x", Port: 443, Secret: secret}
	if got := p.FakeTLSDomain(); got != "example.com" {
		t.Fatalf("FakeTLSDomain = %q, want example.com", got)
	}
}

func TestFakeTLSDomainNonEE(t *testing.T) {
	p := Proxy{Secret: "0123456789abcdef0123456789abcdef"}
	if got := p.FakeTLSDomain(); got != "" {
		t.Fatalf("FakeTLSDomain = %q, want empty", got)
	}
}

func TestClassifySecret(t *testing.T) {
	cases := map[string]string{
		"0123456789abcdef0123456789abcdef": TypePlain,
		"dd0123456789abcdef":               TypeDD,
		"ee0123456789abcdef":               TypeEE,
	}
	for s, want := range cases {
		if got := ClassifySecret(s); got != want {
			t.Errorf("ClassifySecret(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestSortByLatency(t *testing.T) {
	in := []Proxy{
		{Server: "c", LatencyMS: 300, Status: StatusReachable},
		{Server: "a", LatencyMS: 100, Status: StatusReachable},
		{Server: "b", LatencyMS: 100, Status: StatusHandshakeOK},
	}
	out := SortByLatency(in)
	if out[0].Server != "b" || out[1].Server != "a" || out[2].Server != "c" {
		t.Fatalf("unexpected order: %s %s %s", out[0].Server, out[1].Server, out[2].Server)
	}
}

func TestResilienceAndResistance(t *testing.T) {
	eeSecret := "ee" + "0123456789abcdef0123456789abcdef" + "6578616d706c652e636f6d" // ee + key + example.com
	fakeTLS := Proxy{Server: "h", Port: 443, Secret: eeSecret, Type: TypeEE, Status: StatusHandshakeOK}
	plain := Proxy{Server: "h", Port: 8080, Secret: "0123456789abcdef0123456789abcdef", Type: TypePlain, Status: StatusReachable}

	fakeTLS.ComputeResilience()
	plain.ComputeResilience()
	if fakeTLS.Resilience <= plain.Resilience {
		t.Fatalf("FakeTLS resilience %d should exceed plain %d", fakeTLS.Resilience, plain.Resilience)
	}
	if !fakeTLS.IsCensorshipResistant() {
		t.Fatal("FakeTLS on 443 with SNI should be censorship-resistant")
	}
	if plain.IsCensorshipResistant() {
		t.Fatal("plain proxy should not be censorship-resistant")
	}

	// In-country reachability raises both resilience and resistance.
	reached := Proxy{Server: "h", Port: 8080, Secret: "dd0123456789abcdef", Type: TypeDD, ReachableFrom: []string{"IR", "RU"}}
	reached.ComputeResilience()
	if !reached.IsCensorshipResistant() {
		t.Fatal("proxy reachable from censored countries must be resistant")
	}
}

func TestIsCensored(t *testing.T) {
	if !IsCensored("IR") || !IsCensored("RU") {
		t.Fatal("IR/RU should be censored")
	}
	if IsCensored("DE") {
		t.Fatal("DE should not be censored")
	}
}

func TestValidate(t *testing.T) {
	good := Proxy{Server: "h", Port: 443, Secret: "0123456789abcdef0123456789abcdef"}
	if err := good.Validate(); err != nil {
		t.Fatalf("good proxy rejected: %v", err)
	}
	bad := Proxy{Server: "", Port: 443, Secret: "0123456789abcdef0123456789abcdef"}
	if bad.Validate() == nil {
		t.Fatal("empty server accepted")
	}
}

func TestValidateRejectsJunkHost(t *testing.T) {
	sec := "0123456789abcdef0123456789abcdef"
	for _, host := range []string{`x"><img src=x onerror=alert(1)>`, "a b.com", "has space", "<script>"} {
		p := Proxy{Server: host, Port: 443, Secret: sec}
		if p.Validate() == nil {
			t.Errorf("junk host accepted: %q", host)
		}
	}
	for _, host := range []string{"1.2.3.4", "proxy.example.com", "a-b_c.telbet.lol"} {
		p := Proxy{Server: host, Port: 443, Secret: sec}
		if err := p.Validate(); err != nil {
			t.Errorf("valid host rejected: %q (%v)", host, err)
		}
	}
}
