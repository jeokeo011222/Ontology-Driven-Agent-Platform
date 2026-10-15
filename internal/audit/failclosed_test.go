package audit

import (
	"context"
	"fmt"
	"testing"

	"odap/internal/platform/errors"
)

// fakeAppender 是注入失败的 Appender。
type fakeAppender struct {
	err   error
	calls int
}

func (f *fakeAppender) Append(_ context.Context, ev Event) (Event, error) {
	f.calls++
	return ev, f.err
}

func auditFailed(err error) bool {
	ae, ok := err.(*errors.AppError)
	return ok && ae.Code == errors.CodeAuditWriteFailed
}

func TestWriteOrBlock(t *testing.T) {
	cases := []struct {
		name         string
		appErr       error
		crit         Criticality
		wantErr      bool
		wantBuffered int
	}{
		{
			name:         "CRITICAL 失败阻断且不写缓冲",
			appErr:       fmt.Errorf("db down"),
			crit:         CriticalityCritical,
			wantErr:      true,
			wantBuffered: 0,
		},
		{
			name:         "NORMAL 失败写缓冲且返回错误",
			appErr:       fmt.Errorf("db down"),
			crit:         CriticalityNormal,
			wantErr:      true,
			wantBuffered: 1,
		},
		{
			name:         "成功路径不触碰缓冲",
			appErr:       nil,
			crit:         CriticalityNormal,
			wantErr:      false,
			wantBuffered: 0,
		},
		{
			name:         "CRITICAL 成功不触碰缓冲",
			appErr:       nil,
			crit:         CriticalityCritical,
			wantErr:      false,
			wantBuffered: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := &fakeAppender{err: c.appErr}
			buf := &MemBuffer{}
			err := WriteOrBlock(context.Background(), app, buf, baseEvent(), c.crit)

			if c.wantErr {
				if err == nil {
					t.Fatal("期望返回错误，实际 nil")
				}
				if !auditFailed(err) {
					t.Fatalf("期望 CodeAuditWriteFailed，得到 %v", err)
				}
			} else if err != nil {
				t.Fatalf("期望成功，实际错误: %v", err)
			}
			if app.calls != 1 {
				t.Fatalf("Append 调用次数 = %d, 期望 1", app.calls)
			}
			if buf.Len() != c.wantBuffered {
				t.Fatalf("缓冲条数 = %d, 期望 %d", buf.Len(), c.wantBuffered)
			}
		})
	}
}

func TestWriteOrBlockNilAppender(t *testing.T) {
	err := WriteOrBlock(context.Background(), nil, &MemBuffer{}, baseEvent(), CriticalityNormal)
	if err == nil {
		t.Fatal("nil Appender 应返回错误")
	}
}
