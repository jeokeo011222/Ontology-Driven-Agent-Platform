import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Button } from '@/components/ui';
import { cn } from '@/lib/cn';
import type { OntologyGraph, OntologyGraphEdge, OntologyGraphNode, OntologyGraphNodeKind } from '@/types';

/**
 * 本体知识图谱的自研 SVG 视图（零图形库）。
 *
 * # 为什么自己画而不是引第三方图库
 *
 * 本仓前端只有 7 个 runtime 依赖，且图的内容是**确定性**的（节点与边全部来自
 * 服务端对 manifest 的投影，见 `internal/ontology/graphload/graph.go`）。力导向布局
 * 每次打开都会得到不同的坐标，同一份本体截图两次对不上，人工核对与复现都无从谈起。
 * 因此这里用**确定性分层布局**：类排一行、其下按所属类分组放实例；形状图三列
 * （形状 → 目标类 → 约束项）。
 *
 * # 交互
 *
 * 滚轮缩放（以指针为锚点）、背景拖拽平移、拖动节点改变其位置、点击节点高亮其关系。
 * 视图坐标（平移/缩放）与节点位置都只存在于本组件内，不落库、不回传服务端——
 * 它们纯属阅读时的临时视角。
 */

/** 节点框宽度。要点行按等宽字体排版，200 足以容纳 `pattern ^HT-[0-9]{4}$` 这类最长行。 */
const NODE_W = 200;
/** 同一层内相邻节点的水平间距。 */
const COL_GAP = 48;
/** 层与层之间的垂直间距。 */
const ROW_GAP = 64;
/** 箭头标记占用的世界坐标长度：连线末端回退这么多，箭头尖才正好落在框边。 */
const ARROW_GAP = 7;
/**
 * 同一对节点之间有多条边时（如 `submittedBy` 与 `approvedBy` 都是 `报销单 → 员工`），
 * 各条边沿垂直于连线方向错开的间距（世界单位）。22 略大于谓词标签的行高，
 * 因此两张标签不会互相压叠。
 */
const EDGE_SPREAD = 22;
/**
 * 连线必须绕开某个节点框时，抬到该框顶边之上多少（世界单位）。要大于标签行高，
 * 谓词标签才不会压在节点上——标签以顶点为基线，留 24 单位即留出一整行空隙。
 */
const OBSTACLE_CLEARANCE = 24;

const MIN_SCALE = 0.2;
const MAX_SCALE = 3;

/** 节点框（世界坐标）。 */
interface Box {
  node: OntologyGraphNode;
  x: number;
  y: number;
  w: number;
  h: number;
}

/** 视图变换：先 translate 再 scale，因此 x/y 是屏幕像素、k 是缩放倍率。 */
interface View {
  x: number;
  y: number;
  k: number;
}

/**
 * 节点高度：标题行 + 可选副标题行 + 若干要点行。
 *
 * 只由内容决定，不依赖渲染结果——若改用 `getBBox()` 量测，首帧会先得到 0，
 * 布局与适配窗口都会抖一次。
 */
function nodeHeight(node: OntologyGraphNode): number {
  const lines = node.details?.length ?? 0;
  const top = node.subtitle ? 52 : 37;
  if (lines === 0) return node.subtitle ? 46 : 32;
  return top + (lines - 1) * 15 + 14;
}

/** 实例节点的归属由其 `rdf:type` 边的目标给出（边两端必然都在 nodes 里）。 */
function typeOwnerOf(edges: OntologyGraphEdge[]): Map<string, string> {
  const owner = new Map<string, string>();
  for (const edge of edges) {
    if (edge.kind === 'type') owner.set(edge.from, edge.to);
  }
  return owner;
}

/**
 * 本体结构图布局：类排成一行；每个类占一列，列宽按该类实例数展开，
 * 实例排在该列下方（因此"同一类的实例"永远彼此相邻，而不会插到别的类底下）。
 */
function layoutOntology(graph: OntologyGraph): Box[] {
  const classes = graph.nodes.filter((node) => node.kind === 'class');
  const instances = graph.nodes.filter((node) => node.kind === 'instance');
  const owner = typeOwnerOf(graph.edges);

  const byClass = new Map<string, OntologyGraphNode[]>();
  const unowned: OntologyGraphNode[] = [];
  for (const instance of instances) {
    const key = owner.get(instance.id);
    if (!key) {
      // 投影保证每个实例都有 rdf:type 边；万一没有，也照样画出来而不是丢掉，
      // 归入末尾一列，让图里"少了一条边"这件事看得见。
      unowned.push(instance);
      continue;
    }
    const list = byClass.get(key);
    if (list) list.push(instance);
    else byClass.set(key, [instance]);
  }

  const columns: { cls: OntologyGraphNode | null; items: OntologyGraphNode[] }[] = classes.map(
    (cls) => ({ cls, items: byClass.get(cls.id) ?? [] }),
  );
  if (unowned.length > 0) columns.push({ cls: null, items: unowned });

  let maxClassH = 0;
  for (const cls of classes) maxClassH = Math.max(maxClassH, nodeHeight(cls));
  const instanceTop = classes.length > 0 ? maxClassH + ROW_GAP : 0;

  const boxes: Box[] = [];
  let cursor = 0;
  for (const column of columns) {
    const slots = Math.max(1, column.items.length);
    const columnWidth = slots * NODE_W + (slots - 1) * COL_GAP;
    if (column.cls) {
      // 类节点在整组实例的正上方居中：一眼就能看出这些实例属于它。
      boxes.push({
        node: column.cls,
        x: cursor + (columnWidth - NODE_W) / 2,
        y: 0,
        w: NODE_W,
        h: nodeHeight(column.cls),
      });
    }
    column.items.forEach((instance, index) => {
      boxes.push({
        node: instance,
        x: cursor + index * (NODE_W + COL_GAP),
        y: instanceTop,
        w: NODE_W,
        h: nodeHeight(instance),
      });
    });
    cursor += columnWidth + COL_GAP;
  }
  return boxes;
}

/** 形状图布局：形状 / 目标类 / 约束项三列，各自纵向居中。 */
function layoutShapes(graph: OntologyGraph): Box[] {
  const columns: OntologyGraphNode[][] = [
    graph.nodes.filter((node) => node.kind === 'shape'),
    graph.nodes.filter((node) => node.kind === 'class'),
    graph.nodes.filter((node) => node.kind === 'constraint'),
  ];

  const boxes: Box[] = [];
  columns.forEach((nodes, columnIndex) => {
    const heights = nodes.map(nodeHeight);
    const total =
      heights.reduce((sum, h) => sum + h, 0) + ROW_GAP * Math.max(0, nodes.length - 1);
    let y = -total / 2;
    nodes.forEach((node, index) => {
      boxes.push({ node, x: columnIndex * (NODE_W + COL_GAP), y, w: NODE_W, h: heights[index] });
      y += heights[index] + ROW_GAP;
    });
  });
  return boxes;
}

function layout(graph: OntologyGraph, kind: GraphKind): Box[] {
  return kind === 'shapes' ? layoutShapes(graph) : layoutOntology(graph);
}

/**
 * 图的两种画法。取值与抽屉里的子视图枚举一致（本体结构 / 形状约束），
 * 因此抽屉可以直接把子视图状态透传过来，不需要维护第二份映射。
 */
export type GraphKind = 'structure' | 'shapes';

function center(box: Box): { x: number; y: number } {
  return { x: box.x + box.w / 2, y: box.y + box.h / 2 };
}

/**
 * 从框中心朝 `toward` 方向射出，求与框边的交点。
 *
 * 连线若直接画到框中心，箭头会被节点盖住；两端都落在框边上才看得出"这条边指向谁"。
 */
function boundaryPoint(box: Box, toward: { x: number; y: number }): { x: number; y: number } {
  const c = center(box);
  const dx = toward.x - c.x;
  const dy = toward.y - c.y;
  if (dx === 0 && dy === 0) return c;
  const sx = dx === 0 ? Number.POSITIVE_INFINITY : box.w / 2 / Math.abs(dx);
  const sy = dy === 0 ? Number.POSITIVE_INFINITY : box.h / 2 / Math.abs(dy);
  const s = Math.min(sx, sy);
  return { x: c.x + dx * s, y: c.y + dy * s };
}

interface EdgeGeometry {
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  mx: number;
  my: number;
  /** 曲线 / 回环的 path 数据（直边时为 undefined）。 */
  path?: string;
}

/**
 * 计算一条边的两端点（落在两个框边上）与谓词标签的落点。
 *
 * 不传 `apex` 时是直边；传入顶点坐标时改成一条二次贝塞尔曲线。控制点取
 * `2 × apex − 中点`——二次贝塞尔在 t=0.5 处的点在控制点与两端的中点，因此控制点
 * 要"过冲"到顶点之外一倍，曲线顶点才会落在 `apex` 附近。端点朝**控制点**方向射出
 * （而不是朝向对方中心），连线离开框边时的方向才与曲线走向一致。
 */
function edgeGeometry(from: Box, to: Box, apex?: { x: number; y: number }): EdgeGeometry {
  const a = center(from);
  const b = center(to);
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const span = Math.hypot(dx, dy);
  if (span <= ARROW_GAP + 2) {
    // 两个框贴得太近（或重叠）：退回中心连线，不硬造一个方向。
    return { x1: a.x, y1: a.y, x2: b.x, y2: b.y, mx: (a.x + b.x) / 2, my: (a.y + b.y) / 2 };
  }
  const mx = (a.x + b.x) / 2;
  const my = (a.y + b.y) / 2;

  if (!apex) {
    const start = boundaryPoint(from, center(to));
    const end = boundaryPoint(to, center(from));
    const ex = end.x - start.x;
    const ey = end.y - start.y;
    const length = Math.hypot(ex, ey);
    if (length <= ARROW_GAP + 2) {
      return { x1: start.x, y1: start.y, x2: end.x, y2: end.y, mx, my };
    }
    return {
      x1: start.x,
      y1: start.y,
      x2: end.x - (ex / length) * ARROW_GAP,
      y2: end.y - (ey / length) * ARROW_GAP,
      mx,
      my,
    };
  }

  const cx = apex.x * 2 - mx;
  const cy = apex.y * 2 - my;
  const start = boundaryPoint(from, { x: cx, y: cy });
  const end = boundaryPoint(to, { x: cx, y: cy });
  const ex = end.x - cx;
  const ey = end.y - cy;
  const length = Math.hypot(ex, ey) || 1;
  const x2 = end.x - (ex / length) * ARROW_GAP;
  const y2 = end.y - (ey / length) * ARROW_GAP;
  // 标签落在曲线顶点（避开连线本身）；两端口回退后顶点位置会略偏，仍取这条曲线的实际顶点。
  return {
    x1: start.x,
    y1: start.y,
    x2,
    y2,
    mx: (start.x + 2 * cx + x2) / 4,
    my: (start.y + 2 * cy + y2) / 4,
    path: `M ${start.x} ${start.y} Q ${cx} ${cy} ${x2} ${y2}`,
  };
}

/** 中点沿法线方向偏移 `offset`：同一对节点间的多条边按序对称展开时的曲线顶点。 */
function pairApex(from: Box, to: Box, offset: number): { x: number; y: number } {
  const a = center(from);
  const b = center(to);
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const span = Math.hypot(dx, dy) || 1;
  return { x: (a.x + b.x) / 2 - (dy / span) * offset, y: (a.y + b.y) / 2 + (dx / span) * offset };
}

/**
 * 线段是否穿过某个框。用 Liang–Barsky 裁剪求线段参数区间与框的交，非空即有交；
 * 端点在框内、以及擦着框边都算穿过——擦边会让"这条边到底指向谁"变得含糊。
 */
function segmentHitsBox(x1: number, y1: number, x2: number, y2: number, box: Box): boolean {
  let t0 = 0;
  let t1 = 1;
  const dx = x2 - x1;
  const dy = y2 - y1;
  const p = [-dx, dx, -dy, dy];
  const q = [x1 - box.x, box.x + box.w - x1, y1 - box.y, box.y + box.h - y1];
  for (let i = 0; i < 4; i += 1) {
    if (p[i] === 0) {
      if (q[i] < 0) return false; // 与该边平行且落在框外
      continue;
    }
    const r = q[i] / p[i];
    if (p[i] < 0) {
      if (r > t1) return false;
      if (r > t0) t0 = r;
    } else {
      if (r < t0) return false;
      if (r < t1) t1 = r;
    }
  }
  return true;
}

/**
 * 直边若从其它节点框上穿过，返回这些框里最靠上的顶边 y；没穿过任何框则返回 null。
 *
 * 为什么必须管：类排成一行时，`报销单 → 预算` 的直边会**从「员工」框里穿过去**。
 * 连线画在节点之下，于是"报销单 → 预算"看起来是一条断掉的线；谓词标签又正好落在
 * 员工框上，压住它的文字。把这条边抬到障碍物上方，两个问题一起消失。
 */
function obstacleTop(from: Box, to: Box, boxes: Box[], straight: EdgeGeometry): number | null {
  let top: number | null = null;
  for (const box of boxes) {
    if (box.node.id === from.node.id || box.node.id === to.node.id) continue;
    if (!segmentHitsBox(straight.x1, straight.y1, straight.x2, straight.y2, box)) continue;
    top = top === null ? box.y : Math.min(top, box.y);
  }
  return top;
}

/** 自反关系（如 `parent_of` 的 domain/range 是同一个类）画成框上方的一个小回环。 */
function selfLoop(box: Box): EdgeGeometry {
  const cx = box.x + box.w / 2;
  const top = box.y;
  return {
    x1: cx - 14,
    y1: top,
    x2: cx + 14,
    y2: top,
    mx: cx,
    my: top - 40,
    path: `M ${cx - 14} ${top} C ${cx - 34} ${top - 46}, ${cx + 34} ${top - 46}, ${cx + 14} ${top}`,
  };
}

const LEGEND: { kind: OntologyGraphNodeKind; text: string }[] = [
  { kind: 'class', text: '类' },
  { kind: 'instance', text: '实例' },
  { kind: 'shape', text: '形状（sh:NodeShape）' },
  { kind: 'constraint', text: '约束项（sh:PropertyShape）' },
];

export interface OntologyGraphViewProps {
  graph: OntologyGraph;
  /** `ontology` = 本体结构图（类 + 关系 + 实例），`shapes` = 形状约束图。 */
  kind: GraphKind;
}

export function OntologyGraphView({ graph, kind }: OntologyGraphViewProps) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const svgRef = useRef<SVGSVGElement | null>(null);

  const [view, setView] = useState<View>({ x: 0, y: 0, k: 1 });
  const [size, setSize] = useState({ w: 0, h: 0 });
  // 用户拖动后的节点位置覆盖。切图（本体图 / 形状图、显示实例开关）时清空：
  // 换了数据集还沿用旧坐标，节点会落在与结构无关的位置上。
  const [moved, setMoved] = useState<Record<string, { x: number; y: number }>>({});
  const [selected, setSelected] = useState<string | null>(null);

  // 鼠标/触摸手势状态放 ref：拖动过程中不该触发重渲染以外的东西。
  const dragRef = useRef<
    | { mode: 'pan'; sx: number; sy: number; ox: number; oy: number }
    | {
        mode: 'node';
        id: string;
        sx: number;
        sy: number;
        ox: number;
        oy: number;
        moved: boolean;
        /** 按下之前它是否已被选中：决定这一下"点选"还是"取消选中"。 */
        wasSelected: boolean;
      }
    | null
  >(null);

  const base = useMemo(() => layout(graph, kind), [graph, kind]);
  const boxes = useMemo(
    () => base.map((box) => (moved[box.node.id] ? { ...box, ...moved[box.node.id] } : box)),
    [base, moved],
  );
  const boxById = useMemo(() => new Map(boxes.map((box) => [box.node.id, box])), [boxes]);

  /**
   * 每条边的几何先算一次，且**与"是否被选中"无关**。拆成两步是必需的：
   * 适配窗口的包围盒必须把边也包进去（否则抬到节点上方的弧线会被裁掉），
   * 而视口又必须在点击节点引发选中态变化时保持不动——若几何与选中态算在一起，
   * 点一下节点就会重跑适配，整张图跳回初始位置。
   */
  const edgeShapes = useMemo(() => {
    // 同一对节点之间的边必须先按对有数地错开：若都按直边画，连线与谓词标签会
    // **精确重叠**（`submittedBy` 与 `approvedBy` 都是 `报销单 → 员工`）。
    // 这里先统计每对的条数，再在组内按出现顺序对称分配偏移量。全部由输入决定，
    // 因此同一份本体每次打开，哪条边在上、哪条在下都一样。
    const pairTotal = new Map<string, number>();
    for (const edge of graph.edges) {
      if (edge.from === edge.to) continue; // 自反关系走回环，不参与分组
      const key = `${edge.from}->${edge.to}`;
      pairTotal.set(key, (pairTotal.get(key) ?? 0) + 1);
    }
    const pairSeen = new Map<string, number>();

    return graph.edges.map((edge, index) => {
      const from = boxById.get(edge.from);
      const to = boxById.get(edge.to);
      // 边两端的 ID 必然出现在 nodes 里（服务端投影的不变量）；真缺了就不画这条线，
      // 也绝不补一个假节点——图里"少一条边"比"多一个凭空节点"更容易发现。
      if (!from || !to) return null;

      let geometry: EdgeGeometry;
      if (edge.from === edge.to) {
        geometry = selfLoop(from);
      } else {
        const key = `${edge.from}->${edge.to}`;
        const order = pairSeen.get(key) ?? 0;
        pairSeen.set(key, order + 1);
        const total = pairTotal.get(key) ?? 1;
        // 条数 1 → 0；2 → ±0.5×间距；3 → 0, ±1×间距；4 → ±0.5, ±1.5×间距。
        const bow = total > 1 ? (order - (total - 1) / 2) * EDGE_SPREAD : 0;

        let apex = bow !== 0 ? pairApex(from, to, bow) : undefined;
        const straight = edgeGeometry(from, to);
        const blockedAt = obstacleTop(from, to, boxes, straight);
        if (blockedAt !== null) {
          // 抬高到障碍物上方；若这条边同时还有同对多边，就在抬高的基准上再叠一层偏移。
          const mid = center(from);
          const other = center(to);
          apex = { x: (mid.x + other.x) / 2, y: blockedAt - OBSTACLE_CLEARANCE + bow };
        }
        geometry = edgeGeometry(from, to, apex);
      }

      return { key: `${edge.from}->${edge.to}-${edge.kind}-${index}`, edge, geometry };
    });
  }, [graph.edges, boxById]);

  // marker 的 id 必须全局唯一：同页同时存在两张图时，`url(#id)` 会互相抢用。
  const uid = useId().replace(/[^a-zA-Z0-9]/g, '');

  const signature = useMemo(() => graph.nodes.map((node) => node.id).join('|'), [graph]);
  useEffect(() => {
    setMoved({});
    setSelected(null);
  }, [signature]);

  // 容器尺寸：抽屉全宽切换、窗口缩放都要重新适配，否则图会留在旧尺寸的坐标里。
  useLayoutEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const measure = () => setSize({ w: host.clientWidth, h: host.clientHeight });
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(host);
    return () => observer.disconnect();
  }, []);

  /**
   * 按内容包围盒适配窗口；内容比视口小时不放大（保持 1:1，文字才不会被拉糊）。
   *
   * 包围盒必须把**边**也算进去：绕开障碍物被抬到节点上方的弧线若不算，它连同它的
   * 谓词标签就会落在可视区之外——"线绕开了"反而变成"线看不见了"。
   * 这里刻意读初始布局 `base` 而不是用户拖动后的 `boxes`：拖动节点不该触发重新适配，
   * 否则一边拖一边整张图往回跳。
   */
  const fit = () => {
    if (base.length === 0 || size.w === 0 || size.h === 0) return;
    let minX = Infinity;
    let minY = Infinity;
    let maxX = -Infinity;
    let maxY = -Infinity;
    const grow = (x: number, y: number) => {
      minX = Math.min(minX, x);
      minY = Math.min(minY, y);
      maxX = Math.max(maxX, x);
      maxY = Math.max(maxY, y);
    };
    for (const box of base) {
      grow(box.x, box.y);
      grow(box.x + box.w, box.y + box.h);
    }
    for (const shape of edgeShapes) {
      if (!shape) continue;
      const g = shape.geometry;
      grow(g.x1, g.y1);
      grow(g.x2, g.y2);
      grow(g.mx, g.my);
    }
    const pad = 28;
    const k = Math.min(
      1,
      (size.w - pad * 2) / Math.max(1, maxX - minX),
      (size.h - pad * 2) / Math.max(1, maxY - minY),
    );
    setView({
      k,
      x: (size.w - (maxX - minX) * k) / 2 - minX * k,
      y: (size.h - (maxY - minY) * k) / 2 - minY * k,
    });
  };

  // 数据或容器尺寸一变就重新适配。刻意不记录"用户是否手动调过视角"：切换
  // 「显示实例」本身就是换了一张图，沿用旧视角只会让人以为图没变。
  useLayoutEffect(() => {
    fit();
    // fit 只依赖 base/size；显式列出以免每次渲染都重置视角。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [base, size.w, size.h]);

  // 滚轮缩放：React 的 onWheel 在根节点上是 passive 的，preventDefault 无效，
  // 会连带滚动外层抽屉。因此用原生监听并显式声明 non-passive。
  useEffect(() => {
    const svg = svgRef.current;
    if (!svg) return;
    const onWheel = (event: WheelEvent) => {
      event.preventDefault();
      const rect = svg.getBoundingClientRect();
      const px = event.clientX - rect.left;
      const py = event.clientY - rect.top;
      setView((prev) => {
        const next = Math.min(MAX_SCALE, Math.max(MIN_SCALE, prev.k * Math.exp(-event.deltaY / 300)));
        // 让指针下的那个世界坐标点在缩放前后保持不动。
        const wx = (px - prev.x) / prev.k;
        const wy = (py - prev.y) / prev.k;
        return { k: next, x: px - wx * next, y: py - wy * next };
      });
    };
    svg.addEventListener('wheel', onWheel, { passive: false });
    return () => svg.removeEventListener('wheel', onWheel);
  }, []);

  const startDrag = (event: React.PointerEvent) => {
    // setPointerCapture 在指针已失效（或并非真实指针来源）时会抛 NotFoundError。
    // 捕获只是为了让指针移到画布外仍能继续拖动，拿不到也不影响点击与拖动本身。
    try {
      svgRef.current?.setPointerCapture(event.pointerId);
    } catch {
      /* 忽略：无捕获时拖动会在指针离开画布后中断，属可接受的降级。 */
    }
  };

  const onBackgroundPointerDown = (event: React.PointerEvent<SVGSVGElement>) => {
    if (event.button !== 0) return;
    startDrag(event);
    dragRef.current = { mode: 'pan', sx: event.clientX, sy: event.clientY, ox: view.x, oy: view.y };
  };

  const onNodePointerDown = (event: React.PointerEvent, box: Box) => {
    if (event.button !== 0) return;
    // 阻止冒泡：否则背景会同时开始平移，拖动节点时整张图也跟着漂。
    event.stopPropagation();
    startDrag(event);
    // 选中在**按下时**就生效（点下去立刻有反馈）。因此这里要记住按下之前的状态：
    // 若"按下前已选中"且这一下没有拖动，才算取消选中；否则单次点击会变成
    // "按下选中 → 抬起取消"，看起来完全没反应。
    const wasSelected = selected === box.node.id;
    dragRef.current = {
      mode: 'node',
      id: box.node.id,
      sx: event.clientX,
      sy: event.clientY,
      ox: box.x,
      oy: box.y,
      moved: false,
      wasSelected,
    };
    setSelected(box.node.id);
  };

  const onPointerMove = (event: React.PointerEvent<SVGSVGElement>) => {
    const drag = dragRef.current;
    if (!drag) return;
    const dx = event.clientX - drag.sx;
    const dy = event.clientY - drag.sy;
    if (drag.mode === 'pan') {
      setView((prev) => ({ ...prev, x: drag.ox + dx, y: drag.oy + dy }));
      return;
    }
    if (Math.abs(dx) > 2 || Math.abs(dy) > 2) drag.moved = true;
    // 屏幕位移换算回世界位移：缩放后 1 像素对应 1/k 个世界单位。
    const k = view.k || 1;
    setMoved((prev) => ({ ...prev, [drag.id]: { x: drag.ox + dx / k, y: drag.oy + dy / k } }));
  };

  const endDrag = () => {
    const drag = dragRef.current;
    dragRef.current = null;
    // 原地按一下已选中的节点 = 取消选中（退出高亮）；点其它节点时选中由 pointerdown 完成。
    if (drag && drag.mode === 'node' && !drag.moved && drag.wasSelected) {
      setSelected(null);
    }
  };

  const incident = useMemo(() => {
    if (!selected) return null;
    const keys = new Set<string>();
    graph.edges.forEach((edge, index) => {
      if (edge.from === selected || edge.to === selected) keys.add(String(index));
    });
    return keys;
  }, [graph.edges, selected]);

  const kindsPresent = useMemo(() => new Set(graph.nodes.map((node) => node.kind)), [graph.nodes]);

  /**
   * 在已算好的几何上只补"选中态"，再分两趟画：**连线画在节点之下、谓词标签画在节点之上**。
   *
   * 若连线与标签同层、都画在节点之前，相邻两个类之间那条短边的标签会被节点框压住两端
   * （`submittedBy` 只剩中间几个字母，看起来像文本被裁掉了）——因为那条边只有节点间
   * 那点间隙那么长，而标签比间隙宽。标签画在节点之上，配合描边打底，就始终可读。
   */
  const edgeViews = useMemo(
    () =>
      edgeShapes.map((shape, index) =>
        shape
          ? {
              ...shape,
              active: incident?.has(String(index)) ?? false,
              dim: incident !== null && !(incident?.has(String(index)) ?? false),
            }
          : null,
      ),
    [edgeShapes, incident],
  );

  if (graph.nodes.length === 0) {
    return (
      <div className="ontology-graph ontology-graph--empty">
        <p className="text-sm muted">这张图没有节点。</p>
      </div>
    );
  }

  return (
    <div className="ontology-graph" ref={hostRef}>
      <svg
        ref={svgRef}
        className="ontology-graph__canvas"
        role="group"
        aria-label={kind === 'shapes' ? '形状约束图' : '本体结构图'}
        onPointerDown={onBackgroundPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onLostPointerCapture={endDrag}
      >
        <defs>
          {(['relation', 'type', 'target', 'property'] as const).map((edgeKind) => (
            <marker
              key={edgeKind}
              id={`${uid}-arrow-${edgeKind}`}
              viewBox="0 0 8 8"
              refX={8}
              refY={4}
              markerWidth={8}
              markerHeight={8}
              markerUnits="userSpaceOnUse"
              orient="auto"
            >
              <path className={`og-arrow og-arrow--${edgeKind}`} d="M0,0 L8,4 L0,8 z" />
            </marker>
          ))}
        </defs>

        <g transform={`translate(${view.x} ${view.y}) scale(${view.k})`}>
          <g className="og-edge-lines">
            {edgeViews.map((item) => {
              if (!item) return null;
              const className = cn(
                'og-edge__line',
                `og-edge__line--${item.edge.kind}`,
                item.active && 'og-edge__line--active',
                item.dim && 'og-edge__line--dim',
              );
              const markerEnd = `url(#${uid}-arrow-${item.edge.kind})`;
              // 曲线（同对多边）与回环（自反关系）都带 path；只有普通直边用 line。
              return item.geometry.path ? (
                <path key={item.key} className={className} d={item.geometry.path} fill="none" markerEnd={markerEnd} />
              ) : (
                <line
                  key={item.key}
                  className={className}
                  x1={item.geometry.x1}
                  y1={item.geometry.y1}
                  x2={item.geometry.x2}
                  y2={item.geometry.y2}
                  markerEnd={markerEnd}
                />
              );
            })}
          </g>

          {boxes.map((box) => {
            const isSelected = selected === box.node.id;
            const details = box.node.details ?? [];
            return (
              <g
                key={box.node.id}
                className={cn('og-node', `og-node--${box.node.kind}`, isSelected && 'og-node--selected')}
                transform={`translate(${box.x} ${box.y})`}
                tabIndex={0}
                role="button"
                aria-pressed={isSelected}
                aria-label={`${box.node.label}${box.node.subtitle ? `（${box.node.subtitle}）` : ''}`}
                onPointerDown={(event) => onNodePointerDown(event, box)}
                onKeyDown={(event) => {
                  if (event.key !== 'Enter' && event.key !== ' ') return;
                  event.preventDefault();
                  setSelected((prev) => (prev === box.node.id ? null : box.node.id));
                }}
              >
                {/* 完整文本（含要点行）放进 title：节点框宽度固定，长取值靠悬停补全。 */}
                <title>{[box.node.label, box.node.subtitle, ...details].filter(Boolean).join('\n')}</title>
                {isSelected ? (
                  <rect className="og-node__ring" x={-5} y={-5} width={box.w + 10} height={box.h + 10} rx={12} />
                ) : null}
                <rect className="og-node__box" width={box.w} height={box.h} rx={8} />
                <text className="og-node__title" x={10} y={20}>
                  {box.node.label}
                </text>
                {box.node.subtitle ? (
                  <text className="og-node__subtitle" x={10} y={35}>
                    {box.node.subtitle}
                  </text>
                ) : null}
                {details.map((line, index) => (
                  <text
                    key={`${box.node.id}-d${index}`}
                    className="og-node__detail"
                    x={10}
                    y={(box.node.subtitle ? 52 : 37) + index * 15}
                  >
                    {line}
                  </text>
                ))}
              </g>
            );
          })}

          {/* 谓词标签最后一趟画：压在节点之上，短边上的长谓词才不会被节点框截掉。 */}
          <g className="og-edge-labels">
            {edgeViews.map((item) =>
              item ? (
                <text
                  key={item.key}
                  className={cn(
                    'og-edge__label',
                    item.active && 'og-edge__label--active',
                    item.dim && 'og-edge__label--dim',
                  )}
                  x={item.geometry.mx}
                  y={item.geometry.my - 4}
                  textAnchor="middle"
                >
                  {item.edge.label}
                </text>
              ) : null,
            )}
          </g>
        </g>
      </svg>

      <div className="ontology-graph__hud">
        <span className="ontology-graph__hint">滚轮缩放 · 拖拽平移 · 拖动节点调整位置 · 点击节点高亮关系</span>
        <Button
          size="sm"
          variant="secondary"
          onClick={() => {
            setMoved({});
            setSelected(null);
            fit();
          }}
        >
          重置视图
        </Button>
      </div>

      <ul className="ontology-graph__legend">
        {LEGEND.filter((item) => kindsPresent.has(item.kind)).map((item) => (
          <li key={item.kind} className="ontology-graph__legend-item">
            <span className={cn('ontology-graph__swatch', `og-swatch--${item.kind}`)} aria-hidden="true" />
            {item.text}
          </li>
        ))}
      </ul>
    </div>
  );
}
