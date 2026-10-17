"use strict";

// English strings are the keys; ZH holds the Chinese translations. {0}, {1}…
// are positional placeholders.
const ZH = {
  "just now": "刚刚",
  "{0}m ago": "{0} 分钟前",
  "{0}h ago": "{0} 小时前",
  "{0}d ago": "{0} 天前",
  Cancel: "取消",
  OK: "确定",
  "Topic not found": "找不到这个 Topic",
  "Discard unsaved changes?": "放弃未保存的修改？",
  Username: "用户名",
  Password: "密码",
  "Access token (sp_…)": "访问令牌（sp_…）",
  "Sign in": "登录",
  "Sign in with a token": "使用令牌登录",
  "Admin sign in": "管理员登录",
  "Shared pads for people and their agents.": "人和 agent 一起写的共享草稿本。",
  "Open threads": "未解决的讨论",
  Search: "搜索",
  Topics: "Topics",
  "New topic": "新建 Topic",
  "No topics yet": "还没有 Topic",
  Inbox: "收件箱",
  Menu: "菜单",
  "Create a topic to start scratching.": "新建一个 Topic，开始写点什么吧。",
  "Ask your owner to grant you a topic.": "请让你的主人给你开一个 Topic 的权限。",
  Save: "保存",
  Comments: "评论",
  Edit: "编辑",
  More: "更多",
  "New folder": "新建文件夹",
  "New pad": "新建页面",
  "{0} updated {1}": "{0} 更新于 {1}",
  "rev {0}": "版本 {0}",
  "Empty pad.": "空白页面。",
  "{0} folders": "{0} 个文件夹",
  "{0} pads": "{0} 个页面",
  "you can {0}": "你的权限：{0}",
  "Nothing here yet.": "这里还什么都没有。",
  Comment: "评论",
  "Select text to comment, or start a thread above. Mention @user or @user/agent.": "选中正文文字即可批注，或在上方发起讨论。可以 @用户 或 @用户/agent。",
  Close: "关闭",
  Reply: "回复",
  Reopen: "重新打开",
  Resolve: "解决",
  agent: "agent",
  Delete: "删除",
  "Delete this comment?": "删除这条评论？",
  "Reply…": "回复…",
  "Add a comment…": "写评论…",
  "⌘↩ to send": "⌘↩ 发送",
  Saved: "已保存",
  "Someone else changed this pad. Overwrite their changes with yours?": "别人刚改过这个页面。要用你的版本覆盖吗？",
  "A pad with that name already exists": "已经有同名页面了",
  "Someone else changed this pad. Copy your edits, then reload.": "别人刚改过这个页面。请先复制你的修改，再刷新。",
  "Name is required": "请输入名称",
  "name.md": "名称.md",
  "folder name": "文件夹名",
  "Move / rename": "移动 / 重命名",
  History: "历史版本",
  "Delete {0}?": "删除 {0}？",
  "Delete {0} and everything inside?": "删除 {0} 及其中的所有内容？",
  "Pick a revision.": "选择一个版本。",
  View: "查看",
  "slug (a-z, 0-9, -)": "slug（a-z、0-9、-）",
  Title: "标题",
  "Search “{0}”": "搜索“{0}”",
  "No matches.": "没有结果。",
  "Mark all read": "全部标为已读",
  "{0} mentioned you in {1}": "{0} 在 {1} 中提到了你",
  "{0} replied in {1}": "{0} 在 {1} 中回复了",
  "All caught up": "没有新消息",
  "My agents": "我的 agent",
  "Topic access": "Topic 权限",
  Tokens: "令牌",
  Users: "用户",
  Settings: "设置",
  "Sign out": "退出登录",
  "Signed in as {0}": "当前登录：{0}",
  " (admin)": "（管理员）",
  "Agents cannot own agents.": "agent 不能拥有 agent。",
  "Agent group": "agent 组",
  "Agents are named {0}/<slot>. Grant {0}/* to give the whole group access to a topic.":
    "agent 命名为 {0}/<编号>。把 {0}/* 加进 Topic 权限，整组 agent 都能访问。",
  "New token for {0}. Shown once:": "{0} 的新令牌，只显示这一次：",
  "New token": "新令牌",
  "Delete {0} and its tokens?": "删除 {0} 及其所有令牌？",
  "No agents yet.": "还没有 agent。",
  "Created {0}. Token is shown once:": "已创建 {0}。令牌只显示这一次：",
  "Add agent": "添加 agent",
  "slot, e.g. 0, 1, codex": "编号，例如 0、1、codex",
  "Open a topic first.": "请先打开一个 Topic。",
  "Read can view and comment. Write can edit pads. Admin can manage access.": "read 可以查看和评论，write 可以编辑页面，admin 可以管理权限。",
  "alice, alice/1 or alice/*": "alice、alice/1 或 alice/*",
  Remove: "移除",
  Grant: "授权",
  'Type "{0}" to delete this topic and everything in it.': '输入 "{0}" 以删除这个 Topic 及其全部内容。',
  "Delete topic": "删除 Topic",
  "Active tokens": "有效令牌",
  "used {0}": "{0}使用",
  "never used": "从未使用",
  "Revoke token #{0}?": "吊销令牌 #{0}？",
  Revoke: "吊销",
  "{0} agents": "{0} 个 agent",
  "Delete user {0} and all their agents?": "删除用户 {0} 及其所有 agent？",
  "Token for {0}, shown once:": "{0} 的令牌，只显示这一次：",
  "Add user": "添加用户",
  username: "用户名",
  read: "只读",
  write: "可写",
  admin: "管理",
  Language: "语言",
  // Server error messages shown in the UI.
  "not found": "找不到",
  "already exists": "已经存在",
  "admin only": "仅管理员可用",
  "agents cannot do this": "agent 不能执行这个操作",
  "no such topic": "找不到这个 Topic",
  "no such file": "找不到这个页面",
  "no such user": "找不到这个用户",
  "no such agent": "找不到这个 agent",
  "not yours": "没有权限",
  "too many failed attempts, try again later": "失败次数太多，请稍后再试",
  "a new comment must quote text from the pad": "新评论需要先选中正文里的文字",
  "agents can only reply to an existing comment (pass parent=<id>)": "agent 只能回复已有的评论",
  "body is required and must be under 16KB": "内容不能为空，且不超过 16KB",
  "cannot delete yourself": "不能删除自己",
  "folder not empty, pass recursive=1": "文件夹不是空的",
  "Select text in the pad to start a comment.": "选中正文里的文字，就能在这里发起评论。",
  "Agents reply to comments; people start them by selecting text.": "评论由人选中文字发起，agent 只能回复。",
  "Resolved ({0})": "已解决（{0}）",
  "Delete this reply?": "删除这条回复？",
  "Delete this comment and its replies?": "删除这条评论及其所有回复？",
  Copied: "已复制",
  Copy: "复制",
  "Copy failed, select the text instead": "复制失败，请手动选中文字",
  "Delete agent": "删除 agent",
  "Delete user": "删除用户",
  "Delete comment": "删除评论",
  "Revoke token": "吊销令牌",
  "this session": "当前会话",
  "This is the session you are using. Revoking it signs you out.": "这是你正在使用的会话，吊销后会退出登录。",
  " may only use a–z, 0–9 and -": " 只能包含 a–z、0–9 和 -",
  "Wrong token or password": "令牌或密码不正确",
  "Enter a token": "请输入令牌",
  "Enter username and password": "请输入用户名和密码",
  "Save conflict": "保存冲突",
  Overwrite: "覆盖",
  Create: "创建",
  Move: "移动",
};

function detectLang() {
  try {
    const saved = localStorage.getItem("sp_lang");
    if (saved === "zh" || saved === "en") return saved;
  } catch (_) {}
  return /^zh/i.test(navigator.language || "") ? "zh" : "en";
}

let lang = detectLang();
document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";

function setLang(next) {
  lang = next;
  document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
  try {
    localStorage.setItem("sp_lang", lang);
  } catch (_) {}
}

function t(key, ...args) {
  const s = (lang === "zh" && ZH[key]) || key;
  return s.replace(/\{(\d+)\}/g, (_, i) => String(args[+i]));
}

// tx is t() for sentences with element arguments: it returns an array of
// strings and nodes in the translated order.
function tx(key, ...args) {
  const s = (lang === "zh" && ZH[key]) || key;
  return s
    .split(/(\{\d+\})/)
    .filter(Boolean)
    .map((part) => {
      const m = /^\{(\d+)\}$/.exec(part);
      return m ? args[+m[1]] : part;
    });
}
