/**
 * lib/highlight.ts —— 本体产物文本（Turtle / RDF-XML）的分词着色。
 *
 * 为什么自研而不再引入高亮库：前端选型已冻结为「零 UI 组件库 / 无 Tailwind」
 * （第十七篇 §10.3），而这里要着色的文本只有平台自己生成的两三种（Turtle、RDF/XML）。
 * 引入 prismjs / highlight.js 会为这两三种格式拖进几十 KB、一套主题变量和一次
 * 依赖变更；收益只是少写这一个文件。因此这里只覆盖自己生成的那两种语法。
 *
 * **本模块只决定「颜色」，绝不重写文本。** 分词结果拼回去必须与原文逐字节相等，
 * 不相等就整份退回纯文本（见 highlightDocument 末尾的保真校验）。理由：一旦这里
 * 丢字符，"界面上看到的产物"与"库里存的 manifest"就不是同一份东西，而且不报错——
 * 颜色画错的代价是难看，文本丢字的代价是骗人。
 */

/** 着色类别。渲染层把它映射成 .code-view__t--<kind> 类名，本模块不碰样式。 */
export type TokenKind =
  | 'plain' // 空白、缩进、XML 文本节点等不需要着色的部分
  | 'keyword' // @prefix / @base、XML 元素名与 XML 声明目标（<?xml）
  | 'prefix' // rdf:type、sh:targetClass 这类前缀式名称
  | 'iri' // <...> 形式的绝对 IRI
  | 'attr' // XML 属性名
  | 'string' // Turtle 字面值 / XML 属性值
  | 'number' // 数字
  | 'comment' // # 行注释、<!-- --> 块注释
  | 'punct'; // . ; , ^^ < > </> = 等标点

export interface Token {
  kind: TokenKind;
  text: string;
}

/** 平台自己生成的两类文本；与契约里 artifacts 的 rdf / owl 字段对应。 */
export type ArtifactLanguage = 'turtle' | 'xml';

const NAME_START = /[A-Za-z_:]/;
const NAME_REST = /[A-Za-z0-9_.~%-]/;
const TURTLE_PUNCT = ';,.^[]{}()';

type Emit = (kind: TokenKind, text: string) => void;

/**
 * 把一份产物文本切成「每个源行一组 token」。
 *
 * 行数口径：末尾那个换行符不算作额外一行，否则行号栏会多出一个没有内容的号
 * （平台生成的 Turtle / RDF-XML 都以换行结尾，这是常态而非例外）。
 * 跨行的 token（这里只有 XML 的多行标签与多行注释）会按行切开，因此返回值的
 * 第 i 项恰好对应文本的第 i 行，行号即 i + 1。
 */
export function highlightDocument(source: string, language: ArtifactLanguage): Token[][] {
  const body = source.endsWith('\n') ? source.slice(0, -1) : source;
  const lines: Token[][] = [[]];

  /** 当前最后一行的 token 数组（保证一定存在，避免索引访问的类型噪音）。 */
  const tail = (): Token[] => {
    const last = lines[lines.length - 1];
    if (last) return last;
    const created: Token[] = [];
    lines.push(created);
    return created;
  };

  const emit: Emit = (kind, text) => {
    if (text === '') return;
    const parts = text.split('\n');
    for (let p = 0; p < parts.length; p += 1) {
      if (p > 0) lines.push([]);
      const part = parts[p] ?? '';
      if (part === '') continue;
      const current = tail();
      const prev = current[current.length - 1];
      // 合并相邻同类片段：DOM 节点数量大致减半，滚动时不容易掉帧。
      if (prev && prev.kind === kind) prev.text += part;
      else current.push({ kind, text: part });
    }
  };

  if (language === 'turtle') scanTurtle(body, emit);
  else scanXml(body, emit);

  const rebuilt = lines.map((tokens) => tokens.map((t) => t.text).join('')).join('\n');
  if (rebuilt !== body) {
    return body.split('\n').map((line) => (line === '' ? [] : [{ kind: 'plain' as TokenKind, text: line }]));
  }
  return lines;
}

/* ------------------------------------------------------------------ Turtle */

/**
 * Turtle 扫描。逐字符推进，保证"每个字符恰好进入一个 token"。
 *
 * 顺序很重要：`#` 在 Turtle 里既是注释符，也大量出现在 IRI 里
 * （`<http://www.w3.org/2001/XMLSchema#string>`），所以必须先处理 `<...>` 与字符串，
 * 再把剩下的 `#` 当注释——反过来会把每个带 # 的 IRI 后半段整行涂成注释色。
 */
function scanTurtle(body: string, emit: Emit) {
  let i = 0;
  while (i < body.length) {
    const ch = body.charAt(i);

    if (ch === '#') {
      const end = lineEnd(body, i);
      emit('comment', body.slice(i, end));
      i = end;
      continue;
    }
    if (ch === '@') {
      // @prefix / @base：Turtle 里只有这一种以 @ 开头的东西。
      const matched = /^@[A-Za-z]+/.exec(body.slice(i));
      if (matched) {
        emit('keyword', matched[0]);
        i += matched[0].length;
        continue;
      }
    }
    if (ch === '<') {
      const end = body.indexOf('>', i + 1);
      if (end === -1) {
        // 未闭合的 IRI（本平台不会产出，但外部导入的 manifest 可能）：整段按 IRI 显示，
        // 文本一个字符都不丢。
        emit('iri', body.slice(i));
        return;
      }
      emit('iri', body.slice(i, end + 1));
      i = end + 1;
      continue;
    }
    if (ch === '"') {
      const end = stringEnd(body, i);
      emit('string', body.slice(i, end));
      i = end;
      continue;
    }
    if (ch === '^' && body.charAt(i + 1) === '^') {
      emit('punct', '^^');
      i += 2;
      continue;
    }
    if (NAME_START.test(ch)) {
      // 前缀名（`:ExpenseClaim.amount`、`rdf:type`）与裸名（`a`）都走这里。
      // 名称里允许 `.`，所以 `:ExpenseClaim.amount` 会被整体吃掉——
      // 这正是本平台生成的形式，不需要把 `.` 再拆成语句结束符。
      let j = i + 1;
      while (j < body.length && NAME_REST.test(body.charAt(j))) j += 1;
      const name = body.slice(i, j);
      emit(name.includes(':') ? 'prefix' : 'plain', name);
      i = j;
      continue;
    }
    if (isDigit(ch) || (ch === '-' && isDigit(body.charAt(i + 1)))) {
      let j = i + 1;
      while (j < body.length && isDigit(body.charAt(j))) j += 1;
      if (body.charAt(j) === '.' && isDigit(body.charAt(j + 1))) {
        j += 1;
        while (j < body.length && isDigit(body.charAt(j))) j += 1;
      }
      emit('number', body.slice(i, j));
      i = j;
      continue;
    }
    if (TURTLE_PUNCT.includes(ch)) {
      emit('punct', ch);
      i += 1;
      continue;
    }
    emit('plain', ch);
    i += 1;
  }
}

/* ---------------------------------------------------------------- RDF/XML */

/**
 * RDF/XML 扫描。需要跨行状态：本平台生成的 `<rdf:RDF` 会把 5 行 xmlns 声明
 * 都写在同一个标签里，逐行独立扫描会把这 5 行当成普通文本，颜色断层。
 *
 * 文本节点里的 `<` 一定是标签起点——生成端把所有 `<` 都转义成了 `&lt;`，
 * 属性值里的引号同样被转义，因此"遇到引号即属性值结束"是安全的。
 */
function scanXml(body: string, emit: Emit) {
  type State = 'text' | 'tag' | 'value' | 'comment';
  let state: State = 'text';
  let quote = '"';
  /** 标签内的下一段名称是元素名（否则是属性名）：`<` 与 `</` 之后为 true。 */
  let nameIsElement = false;
  let i = 0;

  while (i < body.length) {
    if (state === 'comment') {
      const end = body.indexOf('-->', i);
      const stop = end === -1 ? body.length : end + 3;
      emit('comment', body.slice(i, stop));
      i = stop;
      state = 'text';
      continue;
    }
    if (state === 'value') {
      const end = body.indexOf(quote, i);
      const stop = end === -1 ? body.length : end + 1;
      emit('string', body.slice(i, stop));
      i = stop;
      if (end !== -1) state = 'tag';
      continue;
    }
    if (state === 'tag') {
      const ch = body.charAt(i);
      if (ch === '?') {
        // XML 声明收尾 `?>`：标签态里只有这一处会出现 `?`。
        const closes = body.charAt(i + 1) === '>';
        emit('punct', closes ? '?>' : '?');
        i += closes ? 2 : 1;
        if (closes) {
          state = 'text';
          nameIsElement = false;
        }
        continue;
      }
      if (ch === '>') {
        emit('punct', '>');
        i += 1;
        state = 'text';
        nameIsElement = false;
        continue;
      }
      if (ch === '/') {
        emit('punct', '/');
        i += 1;
        continue;
      }
      if (ch === '=') {
        emit('punct', '=');
        i += 1;
        continue;
      }
      if (ch === '"' || ch === "'") {
        quote = ch;
        emit('string', ch);
        i += 1;
        state = 'value';
        continue;
      }
      if (NAME_START.test(ch)) {
        let j = i + 1;
        while (j < body.length && NAME_REST.test(body.charAt(j))) j += 1;
        emit(nameIsElement ? 'keyword' : 'attr', body.slice(i, j));
        i = j;
        nameIsElement = false;
        continue;
      }
      emit('plain', ch);
      i += 1;
      continue;
    }

    if (body.startsWith('<!--', i)) {
      emit('comment', '<!--');
      i += 4;
      state = 'comment';
      continue;
    }
    if (body.startsWith('<?', i)) {
      emit('keyword', '<?');
      i += 2;
      // 声明的目标名（xml）按元素名着色，后面紧跟的 version / encoding 才是属性。
      state = 'tag';
      nameIsElement = true;
      continue;
    }
    if (body.charAt(i) === '<') {
      emit('punct', '<');
      i += 1;
      state = 'tag';
      nameIsElement = true;
      continue;
    }
    const next = body.indexOf('<', i);
    const stop = next === -1 ? body.length : next;
    emit('plain', body.slice(i, stop));
    i = stop;
  }
}

/* ------------------------------------------------------------------ 辅助 */

function isDigit(ch: string): boolean {
  return ch >= '0' && ch <= '9';
}

function lineEnd(body: string, from: number): number {
  const idx = body.indexOf('\n', from);
  return idx === -1 ? body.length : idx;
}

/** 返回字符串字面值（含收尾引号）的结束位置；未闭合时返回到文本末尾。 */
function stringEnd(body: string, start: number): number {
  const triple = body.startsWith('"""', start);
  const quote = triple ? '"""' : '"';
  let i = start + quote.length;
  while (i < body.length) {
    const ch = body.charAt(i);
    if (ch === '\\') {
      // 反斜杠转义：跳过下一个字符，避免 \" 被当成收尾引号。
      i += 2;
      continue;
    }
    if (triple ? body.startsWith('"""', i) : ch === '"') return i + quote.length;
    i += 1;
  }
  return body.length;
}
