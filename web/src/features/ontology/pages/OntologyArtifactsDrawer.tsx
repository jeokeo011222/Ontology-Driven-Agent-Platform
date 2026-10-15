import { useState } from 'react';
import { Icon } from '@/components/icon';
import { Button, CodeView, Drawer, Input, TabPanel, Tabs } from '@/components/ui';
import type { ArtifactLanguage } from '@/lib/highlight';
import { useOntologyArtifacts } from '../api';
import { OntologyGraphView } from '../components/OntologyGraphView';

/**
 * 四个页签：知识图谱 + 三份原文。
 *
 * 图放在最前并作为默认页签：它是这份产物最容易被读懂的形态；源码页签保留原文，
 * 是因为图上的每个节点、每条边都要能回到原文逐条核对（二者同源，见图组件注释）。
 */
type ArtifactTab = 'graph' | 'rdf' | 'owl' | 'shacl';

const TAB_ITEMS: { id: ArtifactTab; label: string }[] = [
  { id: 'graph', label: '知识图谱' },
  { id: 'rdf', label: 'RDF' },
  { id: 'owl', label: 'OWL' },
  { id: 'shacl', label: 'SHACL' },
];

/** 知识图谱的两种画法：本体结构（类/关系/实例）与形状约束（形状→目标类→约束项）。 */
type GraphView = 'structure' | 'shapes';

const GRAPH_VIEW_ITEMS: { id: GraphView; label: string }[] = [
  { id: 'structure', label: '本体结构' },
  { id: 'shapes', label: '形状约束' },
];

export interface OntologyArtifactsDrawerProps {
  /** 关闭时为 null（抽屉自身用 open 控制显隐，避免额外维护一份布尔状态）。 */
  ontologyId: string | null;
  /** 抽屉标题用的本体名（通常是本体包名）。 */
  ontologyName: string;
  onClose: () => void;
}

/**
 * 产物文本的着色语言。
 *
 * 以服务端返回的 media type 为准，而不是硬编码"页签 → 语言"的映射：契约里
 * rdf/shacl 是 Turtle、owl 是 RDF/XML，但一旦服务端换了序列化格式，硬编码会让
 * 一份 XML 按 Turtle 着色——文字还是真的，颜色却是假的，比不着色更容易误导。
 * 认不出的媒体类型返回 null，由调用方按纯文本展示并说明原因。
 */
function languageOf(format: string): ArtifactLanguage | null {
  if (format.includes('turtle')) return 'turtle';
  if (format.includes('xml')) return 'xml';
  return null;
}

/**
 * 本体产物抽屉：知识图谱 + RDF / OWL / SHACL 三份文本。
 *
 * 内容由服务端从 manifest 派生，因此**不需要先跑校验**，DRAFT 版本也看得到；
 * 服务端未装配模型端点、图存储未就绪都不影响本抽屉。
 *
 * 这里不做任何本地拼装或占位文本：拿不到就说拿不到（错误原文透出），
 * 形状为空就明说"该版本未声明形状"，而不是伪造一份看起来正常的 SHACL。
 *
 * 显示上的四点约定：
 *   - 知识图谱页签由图组件画节点与边（类 / 实例 / 形状 / 约束项），可缩放、平移、选中高亮；
 *   - 源码页签带行号、着色与页签内检索（CodeView），检索计数由 CodeView 回报，本组件不另数一遍；
 *   - 检索词跨页签保留（同一条术语通常要在 RDF 与 OWL 各查一遍），命中序号回到第一处；
 *   - 溯源信息（本体键 / 版本 / 命名图）如实展示，便于直接拿去核对图里的数据。
 */
export function OntologyArtifactsDrawer({
  ontologyId,
  ontologyName,
  onClose,
}: OntologyArtifactsDrawerProps) {
  const open = Boolean(ontologyId);
  const query = useOntologyArtifacts(ontologyId ?? undefined, open);
  const [tab, setTab] = useState<ArtifactTab>('graph');
  const [graphView, setGraphView] = useState<GraphView>('structure');
  // 默认只画类与关系：实例往往是最先被追问"这些是真的吗"的部分，
  // 让用户主动打开，比默认糊上一堆样例数据更诚实。
  const [showInstances, setShowInstances] = useState(false);
  const [feedback, setFeedback] = useState<string | null>(null);
  // 全宽切换：默认维持抽屉原宽度，用户主动展开时才铺满视口。
  const [expanded, setExpanded] = useState(false);
  // 页签内检索：检索词、命中总数（CodeView 回报）、当前命中序号。
  const [search, setSearch] = useState('');
  const [matchCount, setMatchCount] = useState(0);
  const [matchCursor, setMatchCursor] = useState(0);

  const data = query.data;
  // 知识图谱页签没有可复制的原文，因此它在文本类操作（复制、检索）里一律视为"无激活文本"。
  const active = data && tab !== 'graph' ? data[tab] : null;
  const keyword = search.trim();
  // 序号先收敛再使用：计数与高亮必须取自同一个值，否则会出现"显示 3/5 却高亮在别处"。
  const currentMatch = matchCount === 0 ? 0 : Math.min(matchCursor, matchCount - 1);
  const hasInstances = (data?.counts.instances ?? 0) > 0;

  const step = (delta: number) => {
    if (matchCount === 0) return;
    setMatchCursor((prev) => (prev + delta + matchCount) % matchCount);
  };

  const copy = async () => {
    if (!active) return;
    try {
      await navigator.clipboard.writeText(active.content);
      setFeedback('已复制到剪贴板');
    } catch (err) {
      // 剪贴板在非安全上下文或用户拒绝授权时不可用；如实说明失败原因，
      // 不显示"已复制"这种与实际不符的结果。
      setFeedback(`复制失败：${err instanceof Error ? err.message : String(err)}`);
    }
  };

  const footer = data ? (
    <>
      <span className="text-xs muted" style={{ marginRight: 'auto' }}>
        术语层 {data.counts.tbox_triples} 三元组 · 实例层 {data.counts.abox_triples} 三元组 ·
        形状 {data.counts.shapes} · 目标类 {data.counts.target_classes} · 实例{' '}
        {data.counts.instances}
      </span>
      {feedback ? <span className="text-xs muted">{feedback}</span> : null}
      {active ? (
        <Button size="sm" variant="secondary" onClick={() => void copy()}>
          复制 {TAB_ITEMS.find((t) => t.id === tab)?.label}
        </Button>
      ) : null}
    </>
  ) : null;

  return (
    <Drawer
      open={open}
      wide={expanded}
      title={`${ontologyName} · 知识图谱`}
      onClose={onClose}
      footer={footer}
      headerExtra={
        <button
          type="button"
          className="dialog__header-btn"
          aria-pressed={expanded}
          aria-label={expanded ? '恢复抽屉宽度' : '全宽显示'}
          title={expanded ? '恢复抽屉宽度' : '全宽显示'}
          onClick={() => setExpanded((value) => !value)}
        >
          <Icon name={expanded ? 'collapse' : 'expand'} size={16} />
        </button>
      }
    >
      {query.isLoading ? <p className="text-sm muted">正在读取本体产物…</p> : null}

      {query.error ? (
        <div className="banner banner--error" role="alert">
          <span>读取本体产物失败：{query.error.message}</span>
        </div>
      ) : null}

      {data ? (
        <>
          {/* 溯源元信息：这三项决定了"这份文本对应图里的哪一份数据"，
              没有它们就只能靠猜。命名图额外标注它不要求已装载（文本由清单派生）。 */}
          <div className="code-meta">
            <span>
              本体 <span className="copy-id">{data.ontology_key}</span>
            </span>
            <span>
              版本{' '}
              <span className="copy-id" title={`版本 ID ${data.version_id}`}>
                {data.version}
              </span>
            </span>
            <span>
              命名图{' '}
              <span className="copy-id" title="产物文本由本体清单派生，不要求该命名图已装载到图存储。">
                {data.graph_iri}
              </span>
            </span>
          </div>

          {data.counts.shapes === 0 ? (
            <div className="banner banner--neutral" role="status">
              <span>
                该版本未声明任何 sh:shapes，SHACL 文本只有前缀声明、形状约束图为空——没有可执行的约束，
                而不是"校验通过"。
              </span>
            </div>
          ) : null}
          <Tabs
            ariaLabel="本体产物页签"
            value={tab}
            onChange={(next) => {
              // 切页签即清掉上一页的复制反馈，避免"RDF 的复制结果"挂在 SHACL 页上；
              // 检索词保留、命中序号归零，因为每一页的命中位置互不相同。
              setFeedback(null);
              setMatchCursor(0);
              setMatchCount(0);
              setTab(next as ArtifactTab);
            }}
            items={TAB_ITEMS}
          />

          <TabPanel id="graph" active={tab === 'graph'}>
            <div className="graph-toolbar">
              {/* 子视图切换用分段控件而不是再叠一层 Tabs：两层一模一样的下划线页签
                  会让人分不清"当前在哪个层级"，而这两者的层级关系是不同的。 */}
              <div className="segmented" role="group" aria-label="知识图谱视图">
                {GRAPH_VIEW_ITEMS.map((item) => (
                  <button
                    key={item.id}
                    type="button"
                    className="segmented__btn"
                    aria-pressed={graphView === item.id}
                    onClick={() => setGraphView(item.id)}
                  >
                    {item.label}
                  </button>
                ))}
              </div>
              <label
                className="graph-toolbar__toggle"
                title={
                  hasInstances
                    ? '在类与关系之上叠加样例实例节点'
                    : '该版本没有声明样例实例，开关无内容可显示'
                }
              >
                <input
                  type="checkbox"
                  checked={showInstances && hasInstances}
                  disabled={!hasInstances}
                  onChange={(event) => setShowInstances(event.target.checked)}
                />
                显示实例
              </label>
            </div>
            {/* 图与源码同源：节点 ID 就是词项 IRI，可在 RDF / OWL 页签里逐条搜到。
                这里不做任何前端过滤——"图里该有什么"只由服务端定义一次。 */}
            <OntologyGraphView
              // key 随视图与实例开关变化：换了图就重置视角与节点位置，
              // 否则沿用旧坐标会让新图的节点落在与结构无关的位置上。
              key={`${graphView}-${showInstances && hasInstances ? 'abox' : 'tbox'}`}
              kind={graphView}
              graph={
                graphView === 'shapes'
                  ? data.graph.shapes
                  : showInstances && hasInstances
                    ? data.graph.ontology
                    : data.graph.ontology_tbox_only
              }
            />
            <p className="text-xs muted" style={{ marginTop: 8 }}>
              {graphView === 'shapes'
                ? '节点 ID 即词项 IRI；形状与约束项可在 SHACL 页签里逐条核对。'
                : '类与关系来自 RDF / OWL 页签的术语层；实例来自同一份清单的实例层。'}
            </p>
          </TabPanel>

          {TAB_ITEMS.filter((item) => item.id !== 'graph').map((item) => {
            const text = data[item.id as 'rdf' | 'owl' | 'shacl'];
            const language = languageOf(text.format);
            return (
              <TabPanel key={item.id} id={item.id} active={tab === item.id}>
                <div className="code-toolbar">
                  <Input
                    className="code-toolbar__field"
                    value={search}
                    aria-label="在本页签内检索"
                    placeholder="在本页签内检索…"
                    onChange={(event) => {
                      setSearch(event.target.value);
                      // 改检索词即回到第一处命中：否则序号停在上一个词的位置，
                      // 会出现"显示 3/5，而视野里根本没有高亮"。
                      setMatchCursor(0);
                    }}
                  />
                  {keyword === '' ? null : matchCount === 0 ? (
                    <span className="code-toolbar__miss">无命中</span>
                  ) : (
                    <span className="code-toolbar__count">
                      {currentMatch + 1} / {matchCount}
                    </span>
                  )}
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={matchCount === 0}
                    onClick={() => step(-1)}
                  >
                    上一个
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={matchCount === 0}
                    onClick={() => step(1)}
                  >
                    下一个
                  </Button>
                </div>
                <p className="text-xs muted" style={{ marginBottom: 8 }}>
                  {text.format}
                </p>
                {language ? (
                  <CodeView
                    content={text.content}
                    language={language}
                    query={search}
                    activeMatch={currentMatch}
                    onMatchCount={setMatchCount}
                  />
                ) : (
                  // 认不出的媒体类型：不着色，并说明原因——不假装它是 Turtle。
                  <>
                    <p className="text-xs" style={{ marginBottom: 8 }}>
                      媒体类型 {text.format} 没有对应的着色规则，以下按纯文本显示。
                    </p>
                    <pre className="code-block">{text.content}</pre>
                  </>
                )}
              </TabPanel>
            );
          })}
        </>
      ) : null}
    </Drawer>
  );
}
