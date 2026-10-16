package reasoning

// gRPC 装配：把 Service 适配为生成的 reasoningpb.ReasoningServer 接口并提供
// 启动助手。pb 转换只做词法↔词项与错误码映射，业务语义全部留在 Service 层。

import (
	"context"
	stderrors "errors"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	reasoningpb "odap/contracts/proto"
	"odap/internal/ontology/graphstore"
	"odap/internal/platform/errors"
	"odap/internal/platform/ids"
)

// Serve 启动 gRPC 服务器并立即返回；stop 触发优雅停机。
// 形如 Serve(addr, svc, log) (stop func(), err error)，供 cmd/api/main.go 装配。
func Serve(addr string, svc *Service, log *slog.Logger) (func(), error) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, errors.Wrap(err, errors.CodeUnavailable, "推理引擎 gRPC：监听 "+addr+" 失败")
	}
	gs := grpc.NewServer()
	reasoningpb.RegisterReasoningServer(gs, &grpcServer{svc: svc, log: log})
	go func() {
		if serveErr := gs.Serve(lis); serveErr != nil && !stderrors.Is(serveErr, grpc.ErrServerStopped) && log != nil {
			log.Error("推理引擎 gRPC：服务异常退出", "error", serveErr.Error())
		}
	}()
	return gs.GracefulStop, nil
}

// grpcServer 实现 reasoningpb.ReasoningServer。
type grpcServer struct {
	reasoningpb.UnimplementedReasoningServer
	svc *Service
	log *slog.Logger
}

// toStatusError 把平台错误映射为 gRPC 状态错误。
func toStatusError(err error) error {
	if err == nil {
		return nil
	}
	var appErr *errors.AppError
	if stderrors.As(err, &appErr) {
		var code codes.Code
		switch appErr.Code {
		case errors.CodeInvalidArgument, errors.CodeValidationFailed:
			code = codes.InvalidArgument
		case errors.CodeNotFound:
			code = codes.NotFound
		case errors.CodeConflict:
			code = codes.AlreadyExists
		case errors.CodeUnavailable:
			code = codes.Unavailable
		case errors.CodeResourceExhausted:
			code = codes.ResourceExhausted
		default:
			code = codes.Internal
		}
		return status.Error(code, appErr.Error())
	}
	return status.Error(codes.Internal, err.Error())
}

// parseTenant 解析并校验租户 UUID。
func parseTenant(tid string) (ids.ID, error) {
	id, err := ids.Parse(tid)
	if err != nil {
		return ids.ID{}, errors.E(errors.CodeInvalidArgument, "tenant_id 不是合法 UUID")
	}
	return id, nil
}

// triplesToPB 把词项三元组投影为 pb 词法三元组。
func triplesToPB(in []graphstore.Triple) []*reasoningpb.Triple {
	out := make([]*reasoningpb.Triple, 0, len(in))
	for _, t := range in {
		out = append(out, &reasoningpb.Triple{
			Subject:   termToLexical(t.S),
			Predicate: termToLexical(t.P),
			Object:    termToLexical(t.O),
		})
	}
	return out
}

func (g *grpcServer) ForwardChain(ctx context.Context, req *reasoningpb.ForwardChainRequest) (*reasoningpb.ForwardChainReply, error) {
	tenantID, err := parseTenant(req.GetTenantId())
	if err != nil {
		return nil, toStatusError(err)
	}
	res, err := g.svc.ForwardChain(ctx, tenantID, req.GetGraphIri(), req.GetRuleKeys(), int(req.GetMaxIterations()))
	if err != nil {
		return nil, toStatusError(err)
	}
	return &reasoningpb.ForwardChainReply{
		AddedTriples: triplesToPB(res.Derived),
		DerivedCount: int64(len(res.Derived)),
		Iterations:   int32(res.Iterations),
		DurationMs:   res.DurationMS,
	}, nil
}

func (g *grpcServer) BackwardChain(ctx context.Context, req *reasoningpb.BackwardChainRequest) (*reasoningpb.BackwardChainReply, error) {
	tenantID, err := parseTenant(req.GetTenantId())
	if err != nil {
		return nil, toStatusError(err)
	}
	goal := req.GetGoal()
	res, err := g.svc.BackwardChain(ctx, tenantID, req.GetGraphIri(), TriplePattern{
		Subject:   goal.GetSubject(),
		Predicate: goal.GetPredicate(),
		Object:    goal.GetObject(),
	}, req.GetRuleKeys())
	if err != nil {
		return nil, toStatusError(err)
	}
	trace := make([]*reasoningpb.ProofStep, 0, len(res.ProofTrace))
	for _, step := range res.ProofTrace {
		premises := make([]*reasoningpb.TriplePattern, 0, len(step.Premises))
		for _, p := range step.Premises {
			premises = append(premises, &reasoningpb.TriplePattern{Subject: p.Subject, Predicate: p.Predicate, Object: p.Object})
		}
		trace = append(trace, &reasoningpb.ProofStep{
			RuleKey:    step.RuleKey,
			Premises:   premises,
			Conclusion: &reasoningpb.Triple{Subject: termToLexical(step.Conclusion.S), Predicate: termToLexical(step.Conclusion.P), Object: termToLexical(step.Conclusion.O)},
		})
	}
	bindingsPB := make([]*reasoningpb.Binding, 0, len(res.Bindings))
	for _, b := range res.Bindings {
		bindingsPB = append(bindingsPB, &reasoningpb.Binding{Values: b})
	}
	return &reasoningpb.BackwardChainReply{
		Proved:     res.Proved,
		ProofTrace: trace,
		Bindings:   bindingsPB,
	}, nil
}

func (g *grpcServer) IncrementalUpdate(ctx context.Context, req *reasoningpb.IncrementalUpdateRequest) (*reasoningpb.IncrementalUpdateReply, error) {
	tenantID, err := parseTenant(req.GetTenantId())
	if err != nil {
		return nil, toStatusError(err)
	}
	added := make([]LexicalTriple, 0, len(req.GetAddedTriples()))
	for _, t := range req.GetAddedTriples() {
		added = append(added, LexicalTriple{Subject: t.GetSubject(), Predicate: t.GetPredicate(), Object: t.GetObject()})
	}
	lexTriples, err := TriplesFromLexical(added)
	if err != nil {
		return nil, toStatusError(err)
	}
	res, err := g.svc.IncrementalUpdate(ctx, tenantID, req.GetGraphIri(), lexTriples)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &reasoningpb.IncrementalUpdateReply{
		DerivedTriples: triplesToPB(res.Derived),
		DerivedCount:   int64(len(res.Derived)),
	}, nil
}

func (g *grpcServer) RegisterRules(ctx context.Context, req *reasoningpb.RegisterRulesRequest) (*reasoningpb.RegisterRulesReply, error) {
	tenantID, err := parseTenant(req.GetTenantId())
	if err != nil {
		return nil, toStatusError(err)
	}
	defs := make([]Rule, 0, len(req.GetRules()))
	for _, r := range req.GetRules() {
		premises := make([]TriplePattern, 0, len(r.GetPremises()))
		for _, p := range r.GetPremises() {
			premises = append(premises, TriplePattern{Subject: p.GetSubject(), Predicate: p.GetPredicate(), Object: p.GetObject()})
		}
		conclusion := make([]TriplePattern, 0, len(r.GetConclusion()))
		for _, c := range r.GetConclusion() {
			conclusion = append(conclusion, TriplePattern{Subject: c.GetSubject(), Predicate: c.GetPredicate(), Object: c.GetObject()})
		}
		defs = append(defs, Rule{
			RuleKey:     r.GetRuleKey(),
			Name:        r.GetName(),
			Kind:        r.GetKind(),
			Premises:    premises,
			Conclusion:  conclusion,
			Description: r.GetDescription(),
		})
	}
	accepted, rejected, err := g.svc.RegisterRules(ctx, tenantID, defs)
	if err != nil {
		return nil, toStatusError(err)
	}
	rejectedPB := make([]*reasoningpb.RuleRejected, 0, len(rejected))
	for _, r := range rejected {
		rejectedPB = append(rejectedPB, &reasoningpb.RuleRejected{RuleKey: r.RuleKey, Reason: r.Reason})
	}
	return &reasoningpb.RegisterRulesReply{Accepted: accepted, Rejected: rejectedPB}, nil
}

func (g *grpcServer) RemoveRules(ctx context.Context, req *reasoningpb.RemoveRulesRequest) (*reasoningpb.RemoveRulesReply, error) {
	tenantID, err := parseTenant(req.GetTenantId())
	if err != nil {
		return nil, toStatusError(err)
	}
	removed, notRemoved, err := g.svc.RemoveRules(ctx, tenantID, req.GetRuleKeys())
	if err != nil {
		return nil, toStatusError(err)
	}
	notRemovedPB := make([]*reasoningpb.RuleRejected, 0, len(notRemoved))
	for _, r := range notRemoved {
		notRemovedPB = append(notRemovedPB, &reasoningpb.RuleRejected{RuleKey: r.RuleKey, Reason: r.Reason})
	}
	return &reasoningpb.RemoveRulesReply{Removed: removed, NotRemoved: notRemovedPB}, nil
}

func (g *grpcServer) Validate(ctx context.Context, req *reasoningpb.ValidateRequest) (*reasoningpb.ValidateReply, error) {
	tenantID, err := parseTenant(req.GetTenantId())
	if err != nil {
		return nil, toStatusError(err)
	}
	report, err := g.svc.Validate(ctx, tenantID, req.GetGraphIri())
	if err != nil {
		return nil, toStatusError(err)
	}
	results := make([]*reasoningpb.ValidateResult, 0, len(report.Results))
	for _, r := range report.Results {
		results = append(results, &reasoningpb.ValidateResult{
			ShapeId:   r.ShapeID,
			FocusNode: r.FocusNode,
			Path:      r.Path,
			Component: r.Component,
			Severity:  r.Severity,
			Message:   r.Message,
		})
	}
	return &reasoningpb.ValidateReply{Conforms: report.Conforms, Results: results}, nil
}

func (g *grpcServer) HealthCheck(ctx context.Context, req *reasoningpb.HealthCheckRequest) (*reasoningpb.HealthCheckReply, error) {
	tenantID := ids.ID{}
	if req != nil && req.GetTenantId() != "" {
		var err error
		if tenantID, err = parseTenant(req.GetTenantId()); err != nil {
			return nil, toStatusError(err)
		}
	}
	statusStr, rulesLoaded, err := g.svc.HealthCheck(ctx, tenantID)
	if err != nil {
		return nil, toStatusError(err)
	}
	return &reasoningpb.HealthCheckReply{Status: statusStr, RulesLoaded: rulesLoaded}, nil
}
