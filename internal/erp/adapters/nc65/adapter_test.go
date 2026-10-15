package nc65

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"odap/internal/erp"
)

// fakeNC65 模拟 NC65 服务端，按测试意图返回不同响应。
// 仅用于测试替身，不参与生产装配。
type fakeNC65 struct {
	submitStatus int
	submitBody   map[string]any
	omitField    bool
}

func (f *fakeNC65) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/nccloud/api/uapbd/pubbusi/getToken", func(w http.ResponseWriter, r *http.Request) {
		// 校验真实登录被调用，且凭据来自配置。
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["usercode"] != "demo" || body["password"] != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{
			"success": true,
			"data":    map[string]any{"access_token": "tok-123", "expires_in": 3600},
		})
	})
	mux.HandleFunc("/obj/psndoc/get", func(w http.ResponseWriter, r *http.Request) {
		// 业务请求必须携带令牌头。
		if r.Header.Get("access_token") != "tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		obj := map[string]any{"pk_psndoc": "E001", "name": "张三", "code": "EMP001", "ts": "3"}
		if f.omitField {
			delete(obj, "name")
		}
		writeJSON(w, map[string]any{"success": true, "data": obj})
	})
	mux.HandleFunc("/op/expense/submit", func(w http.ResponseWriter, r *http.Request) {
		if f.submitStatus >= 400 {
			w.WriteHeader(f.submitStatus)
			if f.submitBody != nil {
				_ = json.NewEncoder(w).Encode(f.submitBody)
			}
			return
		}
		writeJSON(w, f.submitBody)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"status": "HEALTHY"})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func testConfig(srv *httptest.Server) Config {
	return Config{
		BaseURL:    srv.URL,
		Usercode:   "demo",
		Password:   "secret",
		TokenPath:  "/nccloud/api/uapbd/pubbusi/getToken",
		HealthPath: "/health",
		HTTPClient: srv.Client(),
		Objects: map[string]ObjectMapping{
			"Employee": {
				NativeObjectType: "bd_psndoc",
				GetMethod:        http.MethodPost,
				GetPath:          "/obj/psndoc/get",
				IDField:          "pk_psndoc",
				VersionField:     "ts",
				Fields:           map[string]string{"name": "name", "code": "code"},
			},
		},
		Operations: map[string]OperationMapping{
			"expense.submit": {
				NativeAction: "submit",
				Path:         "/op/expense/submit",
			},
		},
	}
}

func TestNewFailsClosed(t *testing.T) {
	cases := map[string]Config{
		"无地址":   {Usercode: "u", Password: "p", Objects: map[string]ObjectMapping{"X": {NativeObjectType: "x", GetPath: "/x"}}},
		"无凭据":   {BaseURL: "http://x", Objects: map[string]ObjectMapping{"X": {NativeObjectType: "x", GetPath: "/x"}}},
		"无对象映射": {BaseURL: "http://x", Usercode: "u", Password: "p"},
	}
	for name, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Fatalf("%s：期望装配失败（fail-closed），实际成功", name)
		}
	}
}

func TestCapabilitiesHonest(t *testing.T) {
	f := &fakeNC65{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ad, err := New(testConfig(srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	caps, err := ad.Capabilities(context.Background(), "nc65")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps.IdempotencyLevel != erp.IdempotencyLevelL3 {
		t.Errorf("未声明幂等级别应默认为 L3，实际 %s", caps.IdempotencyLevel)
	}
	if caps.SupportsOperationLookup {
		t.Error("未配置查询端点时不应声明支持操作查询")
	}
	if len(caps.Objects) != 1 || caps.Objects[0] != "Employee" {
		t.Errorf("Objects = %v", caps.Objects)
	}
}

func TestGetObjectMapsRealResponse(t *testing.T) {
	f := &fakeNC65{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ad, _ := New(testConfig(srv))

	res, err := ad.GetObject(context.Background(), erp.GetObjectRequest{
		TenantID: "t1", ConnectionID: "nc65", ObjectType: "Employee", ObjectID: "E001",
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	obj := res.Object
	if obj.ObjectID != "E001" {
		t.Errorf("ObjectID = %q", obj.ObjectID)
	}
	if obj.SourceVersion != "3" {
		t.Errorf("SourceVersion = %q", obj.SourceVersion)
	}
	if obj.DataQuality != erp.DataQualityVerified {
		t.Errorf("DataQuality = %q，期望 VERIFIED", obj.DataQuality)
	}
	if obj.Fields["name"] != "张三" || obj.Fields["code"] != "EMP001" {
		t.Errorf("Fields = %v", obj.Fields)
	}
	if obj.ExternalRef.NativeID != "E001" || obj.ExternalRef.NativeObjectType != "bd_psndoc" {
		t.Errorf("ExternalRef = %+v", obj.ExternalRef)
	}
}

func TestGetObjectPartialWhenFieldMissing(t *testing.T) {
	f := &fakeNC65{omitField: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ad, _ := New(testConfig(srv))

	res, err := ad.GetObject(context.Background(), erp.GetObjectRequest{
		TenantID: "t1", ConnectionID: "nc65", ObjectType: "Employee", ObjectID: "E001",
	})
	if err != nil {
		t.Fatalf("GetObject: %v", err)
	}
	if res.Object.DataQuality != erp.DataQualityPartial {
		t.Errorf("缺少映射字段时应标注 PARTIAL，实际 %q", res.Object.DataQuality)
	}
	if _, ok := res.Object.Fields["name"]; ok {
		t.Error("缺失字段不应被填充（严禁伪造）")
	}
}

func TestExecuteSuccess(t *testing.T) {
	f := &fakeNC65{submitBody: map[string]any{"success": true, "data": map[string]any{"pk": "BILL-9", "resultVersion": "2"}}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ad, _ := New(testConfig(srv))

	res, err := ad.Execute(context.Background(), erp.ExecuteRequest{
		TenantID: "t1", ConnectionID: "nc65", OperationID: "op-1",
		OperationType: "expense.submit", TargetObjectID: "E001",
		Payload: map[string]any{"amount": 100}, PayloadDigest: "d1", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != erp.StatusSucceeded {
		t.Errorf("Status = %s，期望 SUCCEEDED", res.Status)
	}
	if res.RemoteRef != "BILL-9" || res.ResultVersion != "2" {
		t.Errorf("RemoteRef/ResultVersion = %q/%q", res.RemoteRef, res.ResultVersion)
	}
}

func TestExecuteBizRejected(t *testing.T) {
	f := &fakeNC65{submitBody: map[string]any{"success": false, "message": "单据状态不允许提交"}}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ad, _ := New(testConfig(srv))

	res, err := ad.Execute(context.Background(), erp.ExecuteRequest{
		TenantID: "t1", ConnectionID: "nc65", OperationID: "op-1",
		OperationType: "expense.submit", PayloadDigest: "d1", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != erp.StatusRejected {
		t.Errorf("Status = %s，期望 REJECTED", res.Status)
	}
	if res.Error == nil || res.Error.Message != "单据状态不允许提交" {
		t.Errorf("Error = %+v", res.Error)
	}
}

func TestExecuteServerErrorIsUnknown(t *testing.T) {
	f := &fakeNC65{submitStatus: http.StatusInternalServerError}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	ad, _ := New(testConfig(srv))

	res, err := ad.Execute(context.Background(), erp.ExecuteRequest{
		TenantID: "t1", ConnectionID: "nc65", OperationID: "op-1",
		OperationType: "expense.submit", PayloadDigest: "d1", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != erp.StatusUnknown {
		t.Errorf("5xx 结果应标记 UNKNOWN（绝不静默当失败），实际 %s", res.Status)
	}
}

func TestHealthTokenProbe(t *testing.T) {
	f := &fakeNC65{}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	cfg := testConfig(srv)
	cfg.HealthPath = "" // 走"真实登录"探针
	ad, _ := New(cfg)

	h, err := ad.Health(context.Background(), "nc65")
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.Healthy || h.Status != erp.HealthHealthy {
		t.Errorf("Health = %+v", h)
	}
}
