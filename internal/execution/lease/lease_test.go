package lease

import (
	"testing"
	"time"

	"odap/internal/platform/errors"
)

var t0 = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

// TestIsExpired 表驱动覆盖到期判定边界。
func TestIsExpired(t *testing.T) {
	cases := []struct {
		name string
		l    Lease
		want bool
	}{
		{"未签发（零值）", Lease{Now: t0}, true},
		{"尚在有效期", Lease{Owner: "w1", ExpiresAt: t0.Add(30 * time.Second), Now: t0}, false},
		{"恰好到期视为过期", Lease{Owner: "w1", ExpiresAt: t0, Now: t0}, true},
		{"已过期", Lease{Owner: "w1", ExpiresAt: t0.Add(-time.Second), Now: t0}, true},
		{"剩余 1ns", Lease{Owner: "w1", ExpiresAt: t0.Add(time.Nanosecond), Now: t0}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.l.IsExpired(); got != c.want {
				t.Fatalf("IsExpired = %v, want %v", got, c.want)
			}
		})
	}
}

// TestIsHeld 持有判定同时要求非空 owner 与未过期。
func TestIsHeld(t *testing.T) {
	if (Lease{ExpiresAt: t0.Add(time.Minute), Now: t0}).IsHeld() {
		t.Fatal("empty owner reported as held")
	}
	if !(Lease{Owner: "w1", ExpiresAt: t0.Add(time.Minute), Now: t0}).IsHeld() {
		t.Fatal("valid lease reported as not held")
	}
	if (Lease{Owner: "w1", ExpiresAt: t0, Now: t0}).IsHeld() {
		t.Fatal("expired lease reported as held")
	}
}

// TestFence 代次 fencing 边界。
func TestFence(t *testing.T) {
	if err := Fence(9, 9); err != nil {
		t.Fatalf("equal epoch rejected: %v", err)
	}
	for _, c := range []struct{ cand, holder int64 }{{8, 9}, {10, 9}, {0, 1}} {
		err := Fence(c.cand, c.holder)
		if err == nil {
			t.Fatalf("Fence(%d,%d) accepted, want error", c.cand, c.holder)
		}
		if code := errors.As(err).Code; code != errors.CodeExecLeaseLost {
			t.Fatalf("Fence(%d,%d) code = %s, want %s", c.cand, c.holder, code, errors.CodeExecLeaseLost)
		}
	}
}

// TestAcquireIncrementsEpoch 领取/接管必须递增代次并重置到期时间。
func TestAcquireIncrementsEpoch(t *testing.T) {
	first := Acquire(Lease{}, "w1", t0, 30*time.Second)
	if first.Epoch != 1 || first.Owner != "w1" {
		t.Fatalf("first acquire = %+v, want epoch 1 owner w1", first)
	}
	if !first.ExpiresAt.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("first expiry = %s", first.ExpiresAt)
	}
	// 接管一个已过期租约：代次递增，旧代次立即失效。
	takeover := Acquire(first, "w2", t0.Add(time.Minute), 30*time.Second)
	if takeover.Epoch != 2 || takeover.Owner != "w2" {
		t.Fatalf("takeover = %+v, want epoch 2 owner w2", takeover)
	}
	if err := Fence(first.Epoch, takeover.Epoch); err == nil {
		t.Fatal("stale worker (epoch 1) accepted after takeover (epoch 2)")
	}
	if err := Fence(takeover.Epoch, takeover.Epoch); err != nil {
		t.Fatalf("current holder rejected: %v", err)
	}
}

// TestRenew 续期：持有者正确且在有效期内才放行，且不改变代次。
func TestRenew(t *testing.T) {
	held := Acquire(Lease{}, "w1", t0, 30*time.Second)

	next, err := Renew(held, "w1", held.Epoch, t0.Add(10*time.Second), 30*time.Second)
	if err != nil {
		t.Fatalf("valid renew rejected: %v", err)
	}
	if next.Epoch != held.Epoch {
		t.Fatalf("renew changed epoch: %d -> %d", held.Epoch, next.Epoch)
	}
	if !next.ExpiresAt.Equal(t0.Add(40 * time.Second)) {
		t.Fatalf("renewed expiry = %s, want %s", next.ExpiresAt, t0.Add(40*time.Second))
	}

	// 非持有者。
	if _, err := Renew(held, "w2", held.Epoch, t0.Add(10*time.Second), 30*time.Second); err == nil {
		t.Fatal("renew by non-owner accepted")
	}
	// 代次不匹配（已被接管）。
	if _, err := Renew(held, "w1", held.Epoch+1, t0.Add(10*time.Second), 30*time.Second); err == nil {
		t.Fatal("renew with stale epoch mismatch accepted")
	}
	// 已过期（含恰好到期）。
	for _, at := range []time.Time{t0.Add(30 * time.Second), t0.Add(31 * time.Second)} {
		_, err := Renew(held, "w1", held.Epoch, at, 30*time.Second)
		if err == nil {
			t.Fatalf("renew at %s accepted, want CodeExecLeaseLost", at)
		}
		if code := errors.As(err).Code; code != errors.CodeExecLeaseLost {
			t.Fatalf("renew error code = %s, want %s", code, errors.CodeExecLeaseLost)
		}
	}
}

// TestRelease 归还后立即作废旧持有者写入权。
func TestRelease(t *testing.T) {
	held := Acquire(Lease{}, "w1", t0, 30*time.Second)
	freed := Release(held)
	if freed.Owner != NotHeld {
		t.Fatalf("release kept owner %q", freed.Owner)
	}
	if freed.Epoch != held.Epoch+1 {
		t.Fatalf("release epoch = %d, want %d", freed.Epoch, held.Epoch+1)
	}
	if err := Fence(held.Epoch, freed.Epoch); err == nil {
		t.Fatal("released worker still allowed to write")
	}
}
