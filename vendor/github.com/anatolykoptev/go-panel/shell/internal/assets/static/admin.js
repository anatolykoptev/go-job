(function(){
  // Password toggle on login page
  var btn=document.getElementById('toggle-pw');
  if(btn) btn.addEventListener('click',function(){
    var p=document.getElementById('password');
    p.type=p.type==='password'?'text':'password';
  });

  // Sidebar toggle
  var sb=document.getElementById('sidebar');
  var toggle=document.getElementById('sidebar-toggle');
  if(sb && localStorage.getItem('sb-c')==='1') sb.classList.add('collapsed');
  if(toggle) toggle.addEventListener('click',function(e){
    e.preventDefault();
    sb.classList.toggle('collapsed');
    localStorage.setItem('sb-c', sb.classList.contains('collapsed')?'1':'0');
  });

  // Sidebar nav active state
  document.querySelectorAll('.sidebar-item').forEach(function(a){
    a.addEventListener('click',function(){
      document.querySelectorAll('.sidebar-item').forEach(function(el){el.classList.remove('active')});
      this.classList.add('active');
    });
  });

  // Entity detail toggle: click again to close (safe: replaceChildren clears DOM)
  document.addEventListener('click',function(e){
    var link=e.target.closest('a[hx-get*="/related"]');
    if(!link) return;
    var targetId=link.getAttribute('hx-target');
    if(!targetId) return;
    var target=document.querySelector(targetId);
    if(target && target.childNodes.length>0){
      e.preventDefault();
      e.stopPropagation();
      target.replaceChildren();
      return false;
    }
  },true);

  // Re-bind after HTMX swaps
  document.addEventListener('htmx:afterSwap',function(){
    document.querySelectorAll('.sidebar-item').forEach(function(a){
      a.addEventListener('click',function(){
        document.querySelectorAll('.sidebar-item').forEach(function(el){el.classList.remove('active')});
        this.classList.add('active');
      });
    });
  });

  // gdNormalizePaste — turn single-newline paragraphs into double-newline so
  // LinkedIn / Twitter / Gmail compose surfaces keep them as separate
  // paragraphs (they collapse `\n` to a space and only honour `\n\n` as a
  // paragraph break). Idempotent: if the text already has `\n\n`, returns
  // unchanged. Bullet groups (lines starting with `-`, `*`, `>`, `•`, `1.`,
  // `1)`, or `#` headers) stay tight: consecutive structural lines joined
  // by single `\n`, so social/email surfaces render them as one list.
  function gdNormalizePaste(text){
    if(!text) return text;
    if(text.indexOf('\n\n')>=0) return text;
    var lines=text.split('\n');
    var STRUCT=/^\s*([-*>•]\s|\d+[.)]\s|#{1,6}\s)/;
    var out='';
    for(var i=0;i<lines.length;i++){
      var line=lines[i];
      if(i===0){out+=line;continue;}
      if(line===''){out+='\n';continue;}
      var thisStruct=STRUCT.test(line);
      var prevStruct=STRUCT.test(lines[i-1]);
      out+=(thisStruct&&prevStruct?'\n':'\n\n')+line;
    }
    return out;
  }

  // Copy-button click delegation — CSP-compliant replacement for inline
  // onclick="gdCopy(...)" in grant_detail.html (page CSP is
  // `script-src 'self' 'unsafe-eval'` with no 'unsafe-inline', so inline
  // handlers and inline <script> are blocked). Button must carry
  // data-copy-pre="<pre id>" + data-copy-field="<n>".
  document.addEventListener('click',function(e){
    var btn=e.target.closest('.gd-copy-btn');
    if(!btn) return;
    var preId=btn.getAttribute('data-copy-pre');
    var fieldNum=btn.getAttribute('data-copy-field')||'';
    var pre=preId?document.getElementById(preId):null;
    if(!pre) return;
    var text=gdNormalizePaste(pre.textContent);
    function flash(label){
      btn.textContent=label;
      btn.classList.add('copied');
      var fb=document.getElementById('copy-feedback');
      if(fb){fb.textContent='Field '+fieldNum+' copied';}
      setTimeout(function(){btn.textContent='Copy';btn.classList.remove('copied');},1500);
    }
    if(navigator.clipboard && navigator.clipboard.writeText){
      navigator.clipboard.writeText(text).then(function(){flash('✓ Copied');}).catch(function(){
        var sel=window.getSelection();var range=document.createRange();
        range.selectNodeContents(pre);sel.removeAllRanges();sel.addRange(range);
      });
    }else{
      var sel=window.getSelection();var range=document.createRange();
      range.selectNodeContents(pre);sel.removeAllRanges();sel.addRange(range);
    }
  });

  // Stage select delegation — CSP-compliant replacement for inline
  // onchange="fetch(...)" on the grant-detail Stage dropdown. Select must
  // carry data-grant-stage-id="<id>".
  document.addEventListener('change',function(e){
    var sel=e.target;
    if(!sel || !sel.matches || !sel.matches('select[data-grant-stage-id]')) return;
    var id=sel.getAttribute('data-grant-stage-id');
    if(!id) return;
    fetch('/admin/grants/'+encodeURIComponent(id)+'/stage',{
      method:'PUT',
      headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:'stage='+encodeURIComponent(sel.value),
    });
  });
})();
