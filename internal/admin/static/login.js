(() => {
  const form = document.getElementById("login-form");
  const input = document.getElementById("admin-token");
  const message = document.getElementById("login-message");
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    message.textContent = "";
    const token = input.value;
    input.value = "";
    try {
      const response = await fetch("/admin/login", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token })
      });
      if (!response.ok) {
        message.textContent = "登录失败，请检查令牌并稍后重试。";
        return;
      }
      window.location.assign("/admin/");
    } catch (_) {
      message.textContent = "暂时无法连接服务，请确认本机服务仍在运行。";
    }
  });
})();
