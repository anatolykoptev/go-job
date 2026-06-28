(function(){
  // Password toggle on login page
  var btn=document.getElementById('toggle-pw');
  if(btn) btn.addEventListener('click',function(){
    var p=document.getElementById('password');
    p.type=p.type==='password'?'text':'password';
  });

  // Sidebar toggle — persists to a server-readable cookie (sb-c) so the
  // server can SSR the collapsed class directly, killing the FOUC.
  // The server already applies the collapsed class; JS only handles toggling.
  var sb=document.getElementById('sidebar');
  var toggle=document.getElementById('sidebar-toggle');
  function readSbc(){return (document.cookie.match(/(?:^|;\s*)sb-c=([^;]*)/)||[])[1];}
  // Safety net: if SSR missed the class (e.g. cached page), apply client-side.
  if(sb && readSbc()==='1') sb.classList.add('collapsed');
  if(toggle) toggle.addEventListener('click',function(e){
    e.preventDefault();
    sb.classList.toggle('collapsed');
    var v=sb.classList.contains('collapsed')?'1':'0';
    document.cookie='sb-c='+v+';path=/;max-age=604800;samesite=Lax';
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

;(function(){
  // ---------------------------------------------------------------------------
  // sidebar-group-label collapse — delegated click toggle for collapsible nav
  // groups. Persists the collapsed set in cookie sb-g (URL-encoded, comma-
  // separated group names, collapsed-only to keep the value small).
  // ADDITIVE ONLY: early-returns when e.target is not a .sidebar-group-label
  // button, so pages without the group markup are completely unaffected.
  // Re-init on htmx:afterSwap applies the current cookie state to new content.
  // ---------------------------------------------------------------------------

  var GRP_COOKIE='sb-g';

  // Read the sb-g cookie and return a Set of collapsed group names.
  // Format: comma-delimited list; commas are the delimiter so group names
  // must not contain literal commas (developer-defined labels; enforced by convention).
  function readGroups(){
    var m=document.cookie.match(/(?:^|;\s*)sb-g=([^;]*)/);
    if(!m||!m[1]) return new Set();
    var raw;
    try{raw=decodeURIComponent(m[1]);}catch(ex){raw=m[1];}
    return new Set(raw.split(',').map(function(s){return s.trim();}).filter(Boolean));
  }

  // Persist the Set back to sb-g (URL-encoded whole value, 7-day, SameSite=Lax).
  // Group names must not contain literal commas — a comma would be encoded in the
  // value but decoded as a delimiter when read back. Server-side (resource/render.go
  // chromeStateFrom) applies the same decode-then-split logic.
  function writeGroups(set){
    var val=encodeURIComponent(Array.from(set).join(','));
    document.cookie=GRP_COOKIE+'='+val+';path=/;max-age=604800;samesite=Lax';
  }

  // Apply current cookie state to DOM (idempotent; safe on htmx:afterSwap).
  // SSR already emits data-collapsed="true" for known-collapsed groups;
  // this handles any group swapped in after initial load and reconciles
  // cross-tab cookie drift when called at IIFE end.
  //
  // SSR/JS invariant: a group containing the active item is ALWAYS expanded.
  // This mirrors the server-side guard in toNavGroups (shell/layout.templ):
  //   "if item.Active { out[last].Collapsed = false }"
  // Both sides MUST stay in lockstep — forcing collapse here would hide the
  // current-page wayfinding indicator after JS runs (a reverse flash + a11y bug).
  function groupsApply(){
    var collapsed=readGroups();
    document.querySelectorAll('button.sidebar-group-label[data-group]').forEach(function(btn){
      var name=btn.getAttribute('data-group');
      var group=btn.closest('.sidebar-group');
      if(!group) return;
      // Mirror the SSR guard: always expand a group that contains the active item,
      // regardless of the cookie. Collapsing it hides the active wayfinding link.
      if(group.querySelector('.sidebar-item.active')){
        group.removeAttribute('data-collapsed');
        btn.setAttribute('aria-expanded','true');
        return;
      }
      if(collapsed.has(name)){
        group.setAttribute('data-collapsed','true');
        btn.setAttribute('aria-expanded','false');
      } else {
        group.removeAttribute('data-collapsed');
        btn.setAttribute('aria-expanded','true');
      }
    });
  }

  // Delegated click: toggle data-collapsed and rewrite cookie.
  document.addEventListener('click',function(e){
    var btn=e.target.closest('button.sidebar-group-label[data-group]');
    if(!btn) return;
    var group=btn.closest('.sidebar-group');
    if(!group) return;
    var name=btn.getAttribute('data-group');
    var collapsed=readGroups();
    if(group.hasAttribute('data-collapsed')){
      group.removeAttribute('data-collapsed');
      btn.setAttribute('aria-expanded','true');
      collapsed.delete(name);
    } else {
      group.setAttribute('data-collapsed','true');
      btn.setAttribute('aria-expanded','false');
      collapsed.add(name);
    }
    writeGroups(collapsed);
  });

  // Re-apply after HTMX swaps (mirrors gdSortableInit pattern above).
  document.addEventListener('htmx:afterSwap',groupsApply);
  // Reconcile cross-tab cookie drift on initial load (SSR already covers the
  // normal single-tab flow; this catches cookies mutated in another tab).
  groupsApply();
})();

;(function(){
  // ---------------------------------------------------------------------------
  // sidebar-parent expand/collapse — delegated click toggle for nav items with
  // Children (one-level submenus). Persists collapsed state in cookie sb-s
  // (URL-encoded, comma-separated item IDs, collapsed-only to keep the value small).
  // ADDITIVE ONLY: early-returns when e.target is not inside .sidebar-parent,
  // so pages without submenu markup are completely unaffected.
  // Re-init on htmx:afterSwap applies current cookie state to new content.
  //
  // SSR/JS invariant: a parent carrying data-has-active-child is ALWAYS expanded,
  // mirroring the server-side hasActiveChild guard in layout.templ.
  // Both sides MUST stay in lockstep — collapsing an active-child parent hides
  // the current-page wayfinding link (a11y + UX regression).
  // ---------------------------------------------------------------------------

  var SUB_COOKIE='sb-s';

  // Extract the nav-id from a sidebar-item's inline anchor-name style.
  // Returns '' when the style attribute is absent or has no --nav-<id>.
  function navIdOf(link){
    var style=link&&link.getAttribute('style');
    if(!style) return '';
    var m=style.match(/anchor-name:\s*--nav-([^;"\s]+)/);
    return m?m[1]:'';
  }

  // Read the sb-s cookie and return a Set of collapsed item IDs.
  function readSubs(){
    var m=document.cookie.match(/(?:^|;\s*)sb-s=([^;]*)/);
    if(!m||!m[1]) return new Set();
    var raw;
    try{raw=decodeURIComponent(m[1]);}catch(ex){raw=m[1];}
    return new Set(raw.split(',').map(function(s){return s.trim();}).filter(Boolean));
  }

  // Persist the Set back to sb-s (URL-encoded, 7-day, SameSite=Lax).
  function writeSubs(set){
    var val=encodeURIComponent(Array.from(set).join(','));
    document.cookie=SUB_COOKIE+'='+val+';path=/;max-age=604800;samesite=Lax';
  }

  // Apply current cookie state to DOM. Idempotent; safe on htmx:afterSwap.
  // SSR always renders expanded; this function collapses based on cookie.
  // Active-child parents are always kept expanded (data-has-active-child guard).
  function subsApply(){
    var collapsed=readSubs();
    document.querySelectorAll('.sidebar-parent').forEach(function(parent){
      // Mirror server guard: always expand parents with an active child.
      if(parent.hasAttribute('data-has-active-child')||parent.querySelector('.sidebar-item.active')){
        parent.removeAttribute('data-collapsed');
        return;
      }
      var link=parent.querySelector(':scope > .sidebar-item');
      if(!link) return;
      var id=navIdOf(link);
      if(!id) return;
      if(collapsed.has(id)){
        parent.setAttribute('data-collapsed','true');
      }else{
        parent.removeAttribute('data-collapsed');
      }
    });
  }

  // Delegated click: clicking a parent navLink toggles its submenu.
  // e.preventDefault() stops navigation to parent.URL; users needing the
  // parent page can right-click or keyboard-navigate.
  // Active-child parents are exempt: they stay expanded and navigate normally.
  document.addEventListener('click',function(e){
    var link=e.target.closest('.sidebar-parent > .sidebar-item');
    if(!link) return;
    var parent=link.closest('.sidebar-parent');
    if(!parent) return;
    // Always expand (and navigate) when a child is active.
    if(parent.hasAttribute('data-has-active-child')||parent.querySelector('.sidebar-item.active')) return;
    e.preventDefault();
    var id=navIdOf(link);
    var collapsed=readSubs();
    if(parent.hasAttribute('data-collapsed')){
      parent.removeAttribute('data-collapsed');
      if(id) collapsed.delete(id);
    }else{
      parent.setAttribute('data-collapsed','true');
      if(id) collapsed.add(id);
    }
    writeSubs(collapsed);
  });

  document.addEventListener('htmx:afterSwap',subsApply);
  subsApply();
})();

;(function(){
  // ---------------------------------------------------------------------------
  // Mobile off-canvas drawer — hamburger open / backdrop-click or Esc close.
  // Keyboard pattern borrowed from pm7 setupKeyboardNavigation, re-namespaced.
  // ADDITIVE ONLY: all listeners guard on element presence and .sidebar--open
  // state so desktop layout (≥768px) and pages without the markup are unaffected.
  // CSP-clean: delegated document listeners, no inline handlers.
  // ---------------------------------------------------------------------------
  var sb=document.getElementById('sidebar');
  var overlay=document.getElementById('sidebar-overlay');
  var toggle=document.getElementById('sidebar-mobile-toggle');

  // Set initial aria-expanded state (not emitted by SSR to avoid clashing with
  // server-rendered aria-expanded on group buttons; JS owns this attribute).
  if(toggle) toggle.setAttribute('aria-expanded','false');

  function openDrawer(){
    if(!sb) return;
    sb.classList.add('sidebar--open');
    if(overlay){overlay.classList.add('sidebar-overlay--visible');overlay.removeAttribute('aria-hidden');}
    if(toggle) toggle.setAttribute('aria-expanded','true');
  }

  function closeDrawer(){
    if(!sb) return;
    sb.classList.remove('sidebar--open');
    if(overlay){overlay.classList.remove('sidebar-overlay--visible');overlay.setAttribute('aria-hidden','true');}
    if(toggle) toggle.setAttribute('aria-expanded','false');
  }

  // Delegated hamburger click — toggle open/close.
  document.addEventListener('click',function(e){
    if(!toggle||!e.target.closest('#sidebar-mobile-toggle')) return;
    if(sb&&sb.classList.contains('sidebar--open')){closeDrawer();}else{openDrawer();}
  });

  // Delegated backdrop click — close.
  document.addEventListener('click',function(e){
    if(!overlay||!e.target.closest('#sidebar-overlay')) return;
    closeDrawer();
  });

  // Esc — close drawer and return focus to hamburger.
  document.addEventListener('keydown',function(e){
    if(e.key!=='Escape') return;
    if(!sb||!sb.classList.contains('sidebar--open')) return;
    closeDrawer();
    if(toggle) toggle.focus();
  });
})();
