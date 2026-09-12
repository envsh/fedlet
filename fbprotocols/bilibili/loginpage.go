package bilibili

const loginPageHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8">
<title>哔哩哔哩登录 - fedlet</title>
<style>
body{font-family:system-ui,sans-serif;max-width:600px;margin:3rem auto;padding:0 1rem;color:#222}
h1{font-size:1.3rem}
h2{font-size:1.05rem}
.card{border:1px solid #ddd;border-radius:10px;padding:1.2rem;margin:1rem 0}
.qrimg{width:220px;height:220px;border:1px solid #eee;border-radius:8px}
input{padding:.5rem;width:100%;box-sizing:border-box;margin:.3rem 0;border:1px solid #ccc;border-radius:6px}
button{padding:.55rem 1.4rem;border:0;border-radius:6px;background:#00aeec;color:#fff;cursor:pointer;margin-top:.5rem}
button.blue{background:#0d69d5}
#state{font-weight:600}
.err{color:#b00020}
pre{white-space:normal;word-break:break-all;background:#f6f6f6;padding:.6rem;border-radius:6px}
.hint{color:#666;font-size:.85rem;line-height:1.6}
</style>
</head>
<body>
<h1>哔哩哔哩登录(fedlet bilibili)</h1>
<div class="card">
<p id="state">初始化…</p>
<div id="qrbox" style="display:none">
  <img class="qrimg" id="qr" alt="QR">
  <p>用 <b>哔哩哔哩 App</b> 的「扫一扫」登录;<br>或手机浏览器打开:<pre id="qrl"></pre></p>
</div>
<div id="userbox" style="display:none">登录成功:<b id="user"></b></div>
<div id="failed" class="err" style="display:none"></div>
</div>
<div class="card">
<h2>密码登录</h2>
<div id="pwform">
  <input id="username" type="text" placeholder="手机号 / 邮箱 / 用户名" autocomplete="username">
  <input id="password" type="password" placeholder="密码" autocomplete="current-password">
  <p class="hint">若触发极验人机验证:请在浏览器打开
  <a href="https://www.bilibili.com" target="_blank" rel="noopener">bilibili.com</a> 完成滑块,
  在下方粘贴验证结果(validate/challenge/seccode)后重试。</p>
  <input id="geetest_validate" type="text" placeholder="validate(如无则留空)">
  <input id="geetest_challenge" type="text" placeholder="challenge(如无则留空)">
  <input id="geetest_seccode" type="text" placeholder="seccode(如无则留空)">
  <button onclick="doPassword()">密码登录</button>
</div>
<p id="pwmsg" class="err" style="display:none;white-space:pre-wrap"></p>
</div>
<div class="card">
<h2>手机号 + 验证码</h2>
<div id="phoneform">
  <input id="sms_phone" type="tel" placeholder="手机号" autocomplete="tel">
  <button onclick="doSmsSend()">发送验证码</button>
</div>
<div id="smscodeform" style="display:none">
  <input id="sms_code" type="text" placeholder="短信验证码" autocomplete="one-time-code">
  <button onclick="doSmsLogin()">登录</button>
</div>
<p id="smsmsg" class="err" style="display:none;white-space:pre-wrap"></p>
</div>
<div class="card">
<h2>Cookie 登录(从浏览器复制)</h2>
<details>
<summary style="cursor:pointer;color:#0d69d5">点击展开:获取方法</summary>
<ol class="hint">
<li>电脑浏览器打开 <b>bilibili.com</b> 并登录</li>
<li>F12 打开开发者工具 → <b>Application</b> → <b>Cookies</b> → <code>bilibili.com</code></li>
<li>找到并复制以下字段的值:</li>
<ul>
<li><b>SESSDATA</b>(必填) — 登录会话令牌</li>
<li><b>bili_jct</b>(必填) — CSRF 令牌</li>
<li><b>DedeUserID</b>(可选) — 用户 UID</li>
<li><b>DedeUserID__ckMd5</b>(可选) — UID 校验值</li>
</ul>
<li>refresh_token:F12 → Console,执行 <code>localStorage.getItem('ac_time_value')</code> 回车,
  复制返回值(双引号内)→ 用于会话续期(可选)</li>
<li>注意:SESSDATA 有效期约 30 天,过期后可再行获取;有 refresh_token 时可自动续期</li>
</ol>
</details>
<div id="cookieform">
  <input id="cookie_sessdata" type="text" placeholder="SESSDATA(必填)" autocomplete="off">
  <input id="cookie_jct" type="text" placeholder="bili_jct(必填)" autocomplete="off">
  <input id="cookie_dedeid" type="text" placeholder="DedeUserID(可选)" autocomplete="off">
  <input id="cookie_ckmd5" type="text" placeholder="DedeUserID__ckMd5(可选)" autocomplete="off">
  <input id="cookie_ft" type="text" placeholder="refresh_token(可选)" autocomplete="off">
  <button class="blue" onclick="cookieLogin()">使用 Cookie 登录</button>
</div>
<p id="cookiemsg" class="err" style="display:none;white-space:pre-wrap"></p>
</div>
<script>
const $=id=>document.getElementById(id);
const stage_txt={'idle':'准备二维码…','waiting':'等待扫码…','scanned':'已扫码,确认中…','done':'登录成功','expired':'二维码已过期,请刷新页面','failed':'登录失败'};
async function poll(){
  try{
    const r=await fetch('/api/state');const s=await r.json();
    $('state').textContent=stage_txt[s.stage]||s.stage;
    if(s.qr_code_url){$('qrbox').style.display='';$('qr').src=s.qr_code_url;$('qrl').textContent=s.qr_url;}
    if(s.stage==='done'){ $('qrbox').style.display='none';$('userbox').style.display='';$('user').textContent=s.user||'';}
    if(s.stage==='failed'){ $('failed').style.display='';$('failed').textContent=s.error||'未知错误';}
  }catch(e){}
}
async function doPassword(){
  const username=$('username').value.trim(),password=$('password').value;
  const validate=$('geetest_validate').value.trim(),challenge=$('geetest_challenge').value.trim(),seccode=$('geetest_seccode').value.trim();
  const msg=$('pwmsg'); msg.style.display='none';
  if(!username||!password){ msg.style.display=''; msg.textContent='请输入用户名和密码'; return; }
  try{
    const r=await fetch('/api/password',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({username,password,validate,challenge,seccode})});
    const j=await r.json();
    if(j.ok){ $('pwform').style.display='none'; $('state').textContent='登录成功'; $('userbox').style.display=''; $('user').textContent=j.user||''; }
    else { msg.style.display=''; msg.textContent='密码登录失败:\n'+j.error; }
  }catch(e){ msg.style.display=''; msg.textContent='密码登录失败:\n'+(e&&e.message||'网络错误'); }
}
async function doSmsSend(){
  const phone=$('sms_phone').value.trim(); const msg=$('smsmsg'); msg.style.display='none';
  if(!phone){ msg.style.display=''; msg.textContent='请输入手机号'; return; }
  try{
    const r=await fetch('/api/sms',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phone})});
    const j=await r.json();
    if(j.ok){ $('phoneform').style.display='none'; $('smscodeform').style.display=''; msg.style.display='none'; }
    else { msg.style.display=''; msg.textContent='发送短信失败:\n'+j.error; }
  }catch(e){ msg.style.display=''; msg.textContent='发送短信失败:\n'+(e&&e.message||'网络错误'); }
}
async function doSmsLogin(){
  const phone=$('sms_phone').value.trim(),code=$('sms_code').value.trim(); const msg=$('smsmsg'); msg.style.display='none';
  try{
    const r=await fetch('/api/smslogin',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({phone,code})});
    const j=await r.json();
    if(j.ok){ $('phoneform').style.display='none'; $('smscodeform').style.display='none'; $('state').textContent='登录成功'; $('userbox').style.display=''; $('user').textContent=j.user||''; }
    else { msg.style.display=''; msg.textContent='短信登录失败:\n'+j.error; }
  }catch(e){ msg.style.display=''; msg.textContent='短信登录失败:\n'+(e&&e.message||'网络错误'); }
}
async function cookieLogin(){
  const SESSDATA=$('cookie_sessdata').value.trim(),bili_jct=$('cookie_jct').value.trim();
  const DedeUserID=$('cookie_dedeid').value.trim(),DedeUserID__ckMd5=$('cookie_ckmd5').value.trim();
  const refresh_token=$('cookie_ft').value.trim();
  const msg=$('cookiemsg'); msg.style.display='none';
  if(!SESSDATA||!bili_jct){ msg.style.display=''; msg.textContent='SESSDATA 和 bili_jct 为必填项'; return; }
  try{
    const r=await fetch('/api/cookie',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({SESSDATA,bili_jct,DedeUserID,DedeUserID__ckMd5,refresh_token})});
    const j=await r.json();
    if(j.ok){ $('cookieform').style.display='none'; msg.style.display='none'; $('qrbox').style.display='none';
      $('state').textContent='登录成功'; $('userbox').style.display=''; $('user').textContent=j.user||''; }
    else { msg.style.display=''; msg.textContent='Cookie 登录失败:\n'+j.error; }
  }catch(e){ msg.style.display=''; msg.textContent='Cookie 登录失败:\n'+(e&&e.message||'网络错误'); }
}
poll();setInterval(poll,1500);
</script>
</body>
</html>
`
