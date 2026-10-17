// A small, deliberately strict markdown renderer. Every piece of source text
// is HTML-escaped before any markup is added, and links only allow http(s),
// mailto and in-app relative targets, so rendered output cannot carry script.
(function () {
  const escapeHTML = (s) => s.replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

  function safeURL(url) {
    const u = url.trim();
    if (/^(https?:|mailto:)/i.test(u)) return u;
    if (/^[a-z][a-z0-9+.-]*:/i.test(u)) return null;
    return u;
  }

  function inline(text) {
    const codes = [];
    let s = text.replace(/`([^`]+)`/g, (_, c) => {
      codes.push("<code>" + escapeHTML(c) + "</code>");
      return "\u0000" + (codes.length - 1) + "\u0000";
    });
    const links = [];
    s = s.replace(/!?\[([^\]]*)\]\(((?:[^()\s]|\([^()\s]*\))+)\)/g, (_, label, url) => {
      const href = safeURL(url);
      const inner = escapeHTML(label);
      links.push(href === null ? inner : '<a href="' + escapeHTML(href) + '" target="_blank" rel="noopener noreferrer">' + inner + "</a>");
      return "\u0001" + (links.length - 1) + "\u0001";
    });
    s = escapeHTML(s);
    s = s.replace(
      /(^|[\s(])(https?:\/\/[^\s<]+[^\s<.,;:!?)\]])/g,
      (_, pre, url) => pre + '<a href="' + url + '" target="_blank" rel="noopener noreferrer">' + url + "</a>",
    );
    s = s.replace(/\*\*(?=\S)(.+?)\*\*/g, "<strong>$1</strong>");
    s = s.replace(/(^|[^\w*])\*([^*\s][^*]*)\*/g, "$1<em>$2</em>");
    s = s.replace(/(^|[^\w_])_([^_\s][^_]*)_(?!\w)/g, "$1<em>$2</em>");
    s = s.replace(/~~([^~]+)~~/g, "<del>$1</del>");
    s = s.replace(/(^|[^\w@/])@([a-z][a-z0-9-]{0,31}(?:\/(?:\*|[a-z0-9][a-z0-9-]{0,31}))?)/g, '$1<span class="mention">@$2</span>');
    s = s.replace(/\u0001(\d+)\u0001/g, (_, i) => links[+i]);
    s = s.replace(/\u0000(\d+)\u0000/g, (_, i) => codes[+i]);
    return s;
  }

  function render(src) {
    const lines = src.replace(/\r\n?/g, "\n").split("\n");
    const out = [];
    let i = 0;
    const isBlockStart = (l) => /^(#{1,6}\s|```|>\s?|\s*([-*+]|\d+[.)])\s|(\s*[-*_]){3,}\s*$|\|)/.test(l);
    while (i < lines.length) {
      const line = lines[i];
      if (/^```/.test(line)) {
        const lang = line.slice(3).trim();
        const body = [];
        i++;
        while (i < lines.length && !/^```/.test(lines[i])) body.push(lines[i++]);
        i++;
        out.push("<pre><code" + (lang ? ' data-lang="' + escapeHTML(lang) + '"' : "") + ">" + escapeHTML(body.join("\n")) + "</code></pre>");
        continue;
      }
      let m;
      if ((m = /^(#{1,6})\s+(.*)$/.exec(line))) {
        out.push("<h" + m[1].length + ">" + inline(m[2].replace(/\s+#+\s*$/, "")) + "</h" + m[1].length + ">");
        i++;
        continue;
      }
      if (/^(\s*[-*_]){3,}\s*$/.test(line)) {
        out.push("<hr>");
        i++;
        continue;
      }
      if (/^>\s?/.test(line)) {
        const body = [];
        while (i < lines.length && /^>\s?/.test(lines[i])) body.push(lines[i++].replace(/^>\s?/, ""));
        out.push("<blockquote>" + render(body.join("\n")) + "</blockquote>");
        continue;
      }
      if (/^\|.*\|\s*$/.test(line) && i + 1 < lines.length && /^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/.test(lines[i + 1])) {
        const cells = (l) =>
          l
            .trim()
            .replace(/^\||\|$/g, "")
            .split("|")
            .map((c) => inline(c.trim()));
        const head = cells(line);
        i += 2;
        const rows = [];
        while (i < lines.length && /^\|.*\|\s*$/.test(lines[i])) rows.push(cells(lines[i++]));
        out.push(
          "<table><thead><tr>" +
            head.map((c) => "<th>" + c + "</th>").join("") +
            "</tr></thead><tbody>" +
            rows.map((r) => "<tr>" + r.map((c) => "<td>" + c + "</td>").join("") + "</tr>").join("") +
            "</tbody></table>",
        );
        continue;
      }
      if ((m = /^(\s*)([-*+]|\d+[.)])\s+/.exec(line))) {
        const indent = m[1].length;
        const ordered = /\d/.test(m[2]);
        const start = ordered ? parseInt(m[2], 10) : 1;
        const items = [];
        while (i < lines.length && (m = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/.exec(lines[i])) && m[1].length <= indent) {
          let text = m[3];
          const nested = [];
          i++;
          while (i < lines.length && lines[i].trim() && /^\s*/.exec(lines[i])[0].length > indent) {
            if (nested.length || /^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) nested.push(lines[i++]);
            else text += " " + lines[i++].trim();
          }
          const task = /^\[([ xX])\]\s+(.*)$/.exec(text);
          const sub = nested.length ? render(nested.join("\n")) : "";
          items.push(
            task
              ? '<li class="task"><input type="checkbox" disabled' + (task[1] !== " " ? " checked" : "") + "> <span>" + inline(task[2]) + sub + "</span></li>"
              : "<li>" + inline(text) + sub + "</li>",
          );
        }
        out.push(ordered ? (start !== 1 ? '<ol start="' + start + '">' : "<ol>") + items.join("") + "</ol>" : "<ul>" + items.join("") + "</ul>");
        continue;
      }
      if (!line.trim()) {
        i++;
        continue;
      }
      const para = [];
      while (i < lines.length && lines[i].trim() && !isBlockStart(lines[i])) para.push(lines[i++]);
      if (!para.length) para.push(lines[i++]);
      out.push("<p>" + para.map(inline).join("<br>") + "</p>");
    }
    return out.join("\n");
  }

  window.Markdown = { render, escapeHTML };
})();
