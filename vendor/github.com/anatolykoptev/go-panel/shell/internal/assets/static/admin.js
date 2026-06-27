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

;(function(){
  // ---------------------------------------------------------------------------
  // gd-sortable — delegated drag-and-drop + keyboard reorder for lists marked
  // with class="gd-sortable". ADDITIVE ONLY: all listeners early-return when
  // e.target.closest('.gd-sortable-item') is null, so pages without the markup
  // are completely unaffected. Activates only on:
  //   <ul class="gd-sortable" data-reorder-url="..." data-csrf="...">
  //     <li class="gd-sortable-item" data-id="...">...</li>
  //   </ul>
  //
  // JS adds draggable="true" and tabindex so non-JS clients never see a broken
  // affordance (progressive enhancement).
  // ---------------------------------------------------------------------------

  // -- helpers ----------------------------------------------------------------

  // Post the current DOM order of items to the container's reorder endpoint.
  // Body: csrf_token=<token>&id=<id1>&id=<id2>... (repeated id fields, in order).
  function gdPostOrder(container){
    var url=container.getAttribute('data-reorder-url');
    var csrf=container.getAttribute('data-csrf');
    if(!url||!csrf) return;
    var items=container.querySelectorAll('.gd-sortable-item');
    var parts=['csrf_token='+encodeURIComponent(csrf)];
    for(var i=0;i<items.length;i++){
      var id=items[i].getAttribute('data-id');
      if(id) parts.push('id='+encodeURIComponent(id));
    }
    fetch(url,{
      method:'POST',
      headers:{'Content-Type':'application/x-www-form-urlencoded'},
      body:parts.join('&'),
      credentials:'same-origin'
    }).catch(function(err){
      if(typeof console!=='undefined'&&console.error) console.error('gd-sortable reorder failed',err);
    });
  }

  // Add draggable + tabindex + aria on every .gd-sortable-item in every
  // .gd-sortable container. Idempotent: re-running on already-init'd items is
  // safe (setAttribute/tabIndex are no-ops when value unchanged).
  function gdSortableInit(){
    var containers=document.querySelectorAll('.gd-sortable');
    if(!containers.length) return;
    for(var c=0;c<containers.length;c++){
      var items=containers[c].querySelectorAll('.gd-sortable-item');
      for(var i=0;i<items.length;i++){
        items[i].setAttribute('draggable','true');
        if(!items[i].getAttribute('tabindex')) items[i].setAttribute('tabindex','0');
        items[i].style.cursor='grab';
        items[i].setAttribute('aria-roledescription','sortable item');
      }
    }
  }

  // -- drag state -------------------------------------------------------------

  var gdDragSrc=null; // the item currently being dragged

  // -- delegated listeners ----------------------------------------------------

  // dragstart — record the source item
  document.addEventListener('dragstart',function(e){
    var item=e.target.closest('.gd-sortable-item');
    if(!item) return;
    gdDragSrc=item;
    e.dataTransfer.effectAllowed='move';
  });

  // dragover — reorder live in the DOM as cursor moves; only within same container
  document.addEventListener('dragover',function(e){
    var item=e.target.closest('.gd-sortable-item');
    if(!item||!gdDragSrc||item===gdDragSrc) return;
    // must be in the same .gd-sortable container
    var container=item.closest('.gd-sortable');
    if(!container||!container.contains(gdDragSrc)) return;
    e.preventDefault();
    e.dataTransfer.dropEffect='move';
    // insert before or after based on cursor Y vs item midpoint
    var rect=item.getBoundingClientRect();
    var midY=rect.top+rect.height/2;
    if(e.clientY<midY){
      container.insertBefore(gdDragSrc,item);
    }else{
      var next=item.nextElementSibling;
      if(next) container.insertBefore(gdDragSrc,next);
      else container.appendChild(gdDragSrc);
    }
  });

  // drop — prevent default browser behaviour (e.g. link navigation)
  document.addEventListener('drop',function(e){
    var item=e.target.closest('.gd-sortable-item');
    if(!item&&!gdDragSrc) return;
    e.preventDefault();
    // order already updated live in dragover; just POST
    var container=(gdDragSrc||item).closest('.gd-sortable');
    if(container) gdPostOrder(container);
    gdDragSrc=null;
  });

  // dragend — fallback cleanup (fires even if drop outside, handles reset)
  document.addEventListener('dragend',function(e){
    var item=e.target.closest('.gd-sortable-item');
    if(!item&&!gdDragSrc) return;
    gdDragSrc=null;
  });

  // keydown — Alt+ArrowUp / Alt+ArrowDown for keyboard reorder
  document.addEventListener('keydown',function(e){
    if(!e.altKey) return;
    if(e.key!=='ArrowUp'&&e.key!=='ArrowDown') return;
    var item=document.activeElement&&document.activeElement.closest
      ? document.activeElement.closest('.gd-sortable-item')
      : null;
    if(!item) return;
    var container=item.closest('.gd-sortable');
    if(!container) return;
    e.preventDefault();
    if(e.key==='ArrowUp'){
      var prev=item.previousElementSibling;
      if(prev&&prev.classList.contains('gd-sortable-item')){
        container.insertBefore(item,prev);
        item.focus();
        gdPostOrder(container);
      }
    }else{
      var next=item.nextElementSibling;
      if(next&&next.classList.contains('gd-sortable-item')){
        var afterNext=next.nextElementSibling;
        if(afterNext) container.insertBefore(item,afterNext);
        else container.appendChild(item);
        item.focus();
        gdPostOrder(container);
      }
    }
  });

  // init on load + re-init after HTMX swaps (mirrors sidebar re-bind at ~:43)
  gdSortableInit();
  document.addEventListener('htmx:afterSwap',gdSortableInit);
})();
