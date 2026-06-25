(function (global, factory) {
  typeof exports === 'object' && typeof module !== 'undefined' ? factory(exports) :
  typeof define === 'function' && define.amd ? define(['exports'], factory) :
  (global = typeof globalThis !== 'undefined' ? globalThis : global || self, factory(global.PM7 = {}));
})(this, (function (exports) { 'use strict';

  /**
   * PM7Menu - Vanilla JavaScript menu component with self-healing
   * Handles dropdown menus with keyboard navigation and accessibility
   * Now with self-healing for framework re-renders (React, Vue, etc.)
   */
  class PM7Menu {
    static instances = new WeakMap(); // Use WeakMap for better memory management
    
    constructor(element) {
      // Self-healing: Check if element was re-rendered by framework
      const wasInitialized = element.hasAttribute('data-pm7-menu-initialized');
      const hasInstance = PM7Menu.instances.has(element);
      
      // If initialized but no instance, element was re-rendered
      if (wasInitialized && !hasInstance) {
        console.log('[PM7Menu] Self-healing: Re-initializing menu after framework re-render');
        // Remove the initialized attribute to allow re-initialization
        element.removeAttribute('data-pm7-menu-initialized');
      }
      
      // Check if this element already has a menu instance
      if (PM7Menu.instances.has(element)) {
        return PM7Menu.instances.get(element);
      }
      
      this.element = element;
      
      // Preserve state if this is a re-render
      const preservedState = this.preserveState();
      
      // AI-Agent FIRST: Automatically add pm7-menu class if missing
      if (!this.element.classList.contains('pm7-menu')) {
        this.element.classList.add('pm7-menu');
      }
      
      this.trigger = element.querySelector('.pm7-menu-trigger');
      this.content = element.querySelector('.pm7-menu-content');
      this.items = element.querySelectorAll('.pm7-menu-item');
      this.isOpen = false;
      this.currentIndex = -1;
      this.hoverTimeouts = new Map();
      
      if (!this.trigger || !this.content) {
        return;
      }
      
      // Store this instance
      PM7Menu.instances.set(element, this);
      
      // Store instance reference on element for self-healing
      element._pm7MenuInstance = this;
      
      this.init();
      
      // Restore state if this was a re-render
      if (preservedState) {
        this.restoreState(preservedState);
      }
      
      // Mark as initialized
      element.setAttribute('data-pm7-menu-initialized', 'true');
    }
    
    preserveState() {
      // Try to preserve state from previous instance
      const oldContent = this.element.querySelector('.pm7-menu-content');
      if (!oldContent) return null;
      
      return {
        wasOpen: oldContent.classList.contains('pm7-menu-content--open') || 
                 oldContent.getAttribute('data-state') === 'open',
        triggerExpanded: this.element.querySelector('.pm7-menu-trigger')?.getAttribute('aria-expanded') === 'true'
      };
    }
    
    restoreState(state) {
      if (state.wasOpen) {
        // Use setTimeout to ensure DOM is ready
        setTimeout(() => {
          this.open();
        }, 0);
      }
    }
    
    init() {
      // Remove any existing event listeners to prevent duplicates
      this.cleanup();
      
      // Check if this menu is part of a menu bar
      this.isInMenuBar = this.element.closest('.pm7-menu-bar') !== null;
      
      // Create bound event handlers for proper cleanup
      this.boundHandlers = {
        triggerClick: (e) => {
          e.stopPropagation();
          
          // In menu bars, always open (don't toggle) if another menu is open
          if (this.isInMenuBar && this.isAnyMenuBarMenuOpen() && !this.isOpen) {
            this.open();
          } else {
            this.toggle();
          }
        },
        triggerMouseEnter: () => {
          // Check if any other menu in the bar is open
          if (this.isAnyMenuBarMenuOpen()) {
            this.open();
          }
        },
        outsideClick: (e) => {
          // Check if the click is outside the menu element and not on a submenu
          if (!this.element.contains(e.target) && this.isOpen) {
            // Check if the click is on a submenu that is part of this menu
            const clickedSubmenu = e.target.closest('.pm7-submenu');
            if (!clickedSubmenu || !this.element.contains(clickedSubmenu)) {
              this.close();
            }
          }
        },
        escape: (e) => {
          if (e.key === 'Escape' && this.isOpen) {
            e.stopPropagation();
            this.close();
            this.trigger.focus();
          }
        },
        reposition: () => {
          if (this.isOpen) {
            this.adjustPosition();
          }
        }
      };
      
      // Click handlers
      this.trigger.addEventListener('click', this.boundHandlers.triggerClick);
      
      // Hover handlers for menu bar menus
      if (this.isInMenuBar) {
        this.trigger.addEventListener('mouseenter', this.boundHandlers.triggerMouseEnter);
      }
      
      // Initialize submenu hover handling
      this.initSubmenuHoverHandling();
      
      // Close on outside click
      document.addEventListener('click', this.boundHandlers.outsideClick);
      
      // Keyboard navigation
      this.trigger.addEventListener('keydown', (e) => this.handleTriggerKeyDown(e));
      this.content.addEventListener('keydown', (e) => this.handleMenuKeyDown(e));
      
      // Menu item clicks
      this.items.forEach((item, index) => {
        // Use mousedown to remove hover state INSTANTLY
        item.addEventListener('mousedown', (e) => {
          if (!item.disabled && !item.hasAttribute('disabled') && !item.classList.contains('pm7-menu-item--has-submenu')) {
            // Remove all hover effects immediately
            item.classList.add('pm7-menu-item--clicking');
            // Mark that we're clicking
            this._clickingItem = true;
          }
        });
        
        item.addEventListener('click', (e) => {
          if (!item.disabled && !item.hasAttribute('disabled')) {
            this.handleItemClick(e, item);
          }
          // Reset _clickingItem after the click event has been processed
          this._clickingItem = false;
        });
        
        // Make items focusable
        item.setAttribute('tabindex', '-1');
      });
      
      // Reposition on window resize/scroll
      window.addEventListener('resize', this.boundHandlers.reposition);
      window.addEventListener('scroll', this.boundHandlers.reposition, true);
    }
    
    cleanup() {
      // Remove all event listeners if they exist
      if (this.boundHandlers) {
        this.trigger?.removeEventListener('click', this.boundHandlers.triggerClick);
        this.trigger?.removeEventListener('mouseenter', this.boundHandlers.triggerMouseEnter);
        document.removeEventListener('click', this.boundHandlers.outsideClick);
        document.removeEventListener('keydown', this.boundHandlers.escape);
        window.removeEventListener('resize', this.boundHandlers.reposition);
        window.removeEventListener('scroll', this.boundHandlers.reposition, true);
      }
    }
    
    destroy() {
      this.cleanup();
      this.close();
      PM7Menu.instances.delete(this.element);
      delete this.element._pm7MenuInstance;
    }
    
    toggle() {
      this.isOpen ? this.close() : this.open();
    }
    
    open() {
      // Close all other open menus
      // Since we're using WeakMap, we need to track open menus differently
      document.querySelectorAll('.pm7-menu-content--open').forEach(content => {
        const menu = content.closest('[data-pm7-menu]');
        if (menu && menu._pm7MenuInstance && menu._pm7MenuInstance !== this) {
          menu._pm7MenuInstance.close();
        }
      });
      
      this.isOpen = true;
      this.content.classList.add('pm7-menu-content--open');
      this.content.setAttribute('data-state', 'open'); // Add data-state for better state tracking
      this.trigger.setAttribute('aria-expanded', 'true');
      
      // Add escape handler when menu opens
      document.addEventListener('keydown', this.boundHandlers.escape);
      
      // Check viewport position and adjust if needed
      this.adjustPosition();
      
      // Focus first item
      requestAnimationFrame(() => {
        this.currentIndex = 0;
        this.focusItem(0);
      });
      
      // Dispatch custom event
      this.element.dispatchEvent(new CustomEvent('pm7:menu:open', { 
        detail: { menu: this },
        bubbles: true 
      }));
    }
    
    close() {
      if (!this.isOpen) return;
      
      this.isOpen = false;
      this.content.classList.remove('pm7-menu-content--open');
      this.content.setAttribute('data-state', 'closed');
      this.trigger.setAttribute('aria-expanded', 'false');
      this.currentIndex = -1;
      
      // Remove escape handler when menu closes
      document.removeEventListener('keydown', this.boundHandlers.escape);
      
      // Clear all hover timeouts
      this.hoverTimeouts.forEach(timeout => clearTimeout(timeout));
      this.hoverTimeouts.clear();
      
      // Close all submenus
      const submenuItems = this.element.querySelectorAll('.pm7-menu-item--has-submenu');
      submenuItems.forEach(item => {
        item.setAttribute('data-submenu-open', 'false');
      });
      
      // Remove focus from items
      this.items.forEach(item => {
        item.setAttribute('tabindex', '-1');
        item.classList.remove('pm7-menu-item--clicking');
      });
      
      // Dispatch custom event
      this.element.dispatchEvent(new CustomEvent('pm7:menu:close', { 
        detail: { menu: this },
        bubbles: true 
      }));
    }
    
    handleTriggerKeyDown(e) {
      switch (e.key) {
        case 'Enter':
        case ' ':
        case 'ArrowDown':
          e.preventDefault();
          this.open();
          break;
        case 'ArrowUp':
          e.preventDefault();
          this.open();
          this.currentIndex = this.items.length - 1;
          this.focusItem(this.currentIndex);
          break;
      }
    }
    
    handleMenuKeyDown(e) {
      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault();
          this.focusNext();
          break;
        case 'ArrowUp':
          e.preventDefault();
          this.focusPrevious();
          break;
        case 'Home':
          e.preventDefault();
          this.focusItem(0);
          break;
        case 'End':
          e.preventDefault();
          this.focusItem(this.items.length - 1);
          break;
        case 'Enter':
        case ' ':
          e.preventDefault();
          const currentItem = this.items[this.currentIndex];
          if (currentItem && !currentItem.disabled) {
            currentItem.click();
          }
          break;
        case 'Tab':
          // Close menu on tab
          this.close();
          break;
      }
    }
    
    focusNext() {
      const nextIndex = this.currentIndex + 1;
      if (nextIndex < this.items.length) {
        this.focusItem(nextIndex);
      } else {
        this.focusItem(0); // Wrap to first
      }
    }
    
    focusPrevious() {
      const prevIndex = this.currentIndex - 1;
      if (prevIndex >= 0) {
        this.focusItem(prevIndex);
      } else {
        this.focusItem(this.items.length - 1); // Wrap to last
      }
    }
    
    focusItem(index) {
      const item = this.items[index];
      if (!item) return;
      
      // Only update if different item
      if (this.currentIndex === index) return;
      
      // Remove tabindex from previous item
      if (this.currentIndex >= 0 && this.items[this.currentIndex]) {
        this.items[this.currentIndex].setAttribute('tabindex', '-1');
      }
      
      this.currentIndex = index;
      item.setAttribute('tabindex', '0');
      item.focus();
    }
    
    handleItemClick(e, item) {
      // Close menu immediately for regular items (not submenus)
      if (!item.classList.contains('pm7-menu-item--has-submenu')) {
        this.close();
        this.trigger.focus();
      }
      
      // Handle checkbox items
      if (item.classList.contains('pm7-menu-item--checkbox')) {
        const isChecked = item.getAttribute('data-checked') === 'true';
        item.setAttribute('data-checked', !isChecked);
      }
      
      // Handle radio items
      if (item.classList.contains('pm7-menu-item--radio')) {
        // Uncheck all radio items in the same group
        const radioItems = this.element.querySelectorAll('.pm7-menu-item--radio');
        radioItems.forEach(radio => radio.setAttribute('data-checked', 'false'));
        // Check the clicked item
        item.setAttribute('data-checked', 'true');
      }
      
      // Handle submenu items
      if (item.classList.contains('pm7-menu-item--has-submenu')) {
        e.preventDefault();
        e.stopPropagation();
        // Toggle submenu
        const isOpen = item.getAttribute('data-submenu-open') === 'true';
        item.setAttribute('data-submenu-open', !isOpen);
        return; // Don't close the main menu
      }
      
      // Dispatch custom event
      const event = new CustomEvent('pm7-menu-select', {
        detail: { item, menu: this },
        bubbles: true
      });
      this.element.dispatchEvent(event);
    }
    
    adjustPosition() {
      // Get dimensions
      const triggerRect = this.trigger.getBoundingClientRect();
      const contentRect = this.content.getBoundingClientRect();
      const viewportHeight = window.innerHeight;
      const viewportWidth = window.innerWidth;
      
      // Calculate space above and below
      const spaceBelow = viewportHeight - triggerRect.bottom;
      const spaceAbove = triggerRect.top;
      
      // Check vertical position
      if (contentRect.height > spaceBelow && spaceAbove > spaceBelow) {
        // Not enough space below, but more space above
        this.content.classList.add('pm7-menu-content--top');
      } else {
        // Enough space below or more space below than above
        this.content.classList.remove('pm7-menu-content--top');
      }
      
      // Check horizontal position for end-aligned menus
      if (this.content.classList.contains('pm7-menu-content--end')) {
        const rightEdge = triggerRect.right;
        if (rightEdge < contentRect.width) {
          // Not enough space on the right, switch to left alignment
          this.content.classList.remove('pm7-menu-content--end');
          this.content.classList.add('pm7-menu-content--start');
        }
      }
      
      // Check horizontal position for center-aligned menus
      if (this.content.classList.contains('pm7-menu-content--center')) {
        const centerX = triggerRect.left + (triggerRect.width / 2);
        const menuHalfWidth = contentRect.width / 2;
        
        if (centerX - menuHalfWidth < 0) {
          // Would overflow on the left
          this.content.classList.remove('pm7-menu-content--center');
          this.content.classList.add('pm7-menu-content--start');
        } else if (centerX + menuHalfWidth > viewportWidth) {
          // Would overflow on the right
          this.content.classList.remove('pm7-menu-content--center');
          this.content.classList.add('pm7-menu-content--end');
        }
      }
    }
    
    // Check if any menu in the same menu bar is open
    isAnyMenuBarMenuOpen() {
      if (!this.isInMenuBar) return false;
      
      const menuBar = this.element.closest('.pm7-menu-bar');
      if (!menuBar) return false;
      
      // Check all menus in the same menu bar
      const menusInBar = menuBar.querySelectorAll('.pm7-menu');
      for (const menuEl of menusInBar) {
        // Skip current menu
        if (menuEl === this.element) continue;
        
        // Check if menu content is visible (open)
        const menuContent = menuEl.querySelector('.pm7-menu-content');
        if (menuContent && menuContent.classList.contains('pm7-menu-content--open')) {
          return true;
        }
      }
      return false;
    }
    
    // Initialize submenu hover handling with improved UX
    initSubmenuHoverHandling() {
      const submenuItems = this.element.querySelectorAll('.pm7-menu-item--has-submenu');
      
      submenuItems.forEach((item, index) => {
        const submenu = item.nextElementSibling;
        if (!submenu || !submenu.classList.contains('pm7-submenu')) return;
        
        const timeoutKey = `submenu-${index}`;
        
        // Handle mouse enter on parent item
        item.addEventListener('mouseenter', () => {
          const timeout = this.hoverTimeouts.get(timeoutKey);
          if (timeout) {
            clearTimeout(timeout);
            this.hoverTimeouts.delete(timeoutKey);
          }
          item.setAttribute('data-submenu-open', 'true');
        });
        
        // Handle mouse leave on parent item
        item.addEventListener('mouseleave', (e) => {
          // Check if we're moving to the submenu
          const toElement = e.relatedTarget;
          if (toElement && (submenu.contains(toElement) || submenu === toElement)) {
            return; // Don't close if moving to submenu
          }
          
          // Add small delay before closing
          const timeout = setTimeout(() => {
            item.setAttribute('data-submenu-open', 'false');
            this.hoverTimeouts.delete(timeoutKey);
          }, 100); // 100ms delay
          this.hoverTimeouts.set(timeoutKey, timeout);
        });
        
        // Handle mouse enter on submenu
        submenu.addEventListener('mouseenter', () => {
          const timeout = this.hoverTimeouts.get(timeoutKey);
          if (timeout) {
            clearTimeout(timeout);
            this.hoverTimeouts.delete(timeoutKey);
          }
          item.setAttribute('data-submenu-open', 'true');
        });
        
        // Handle mouse leave on submenu
        submenu.addEventListener('mouseleave', (e) => {
          // Check if we're moving back to the parent
          const toElement = e.relatedTarget;
          if (toElement && (item.contains(toElement) || item === toElement)) {
            return; // Don't close if moving back to parent
          }
          
          // Add small delay before closing
          const timeout = setTimeout(() => {
            item.setAttribute('data-submenu-open', 'false');
            this.hoverTimeouts.delete(timeoutKey);
          }, 100); // 100ms delay
          this.hoverTimeouts.set(timeoutKey, timeout);
        });
      });
    }
  }

  // Auto-initialize menus
  if (typeof document !== 'undefined' && !window.__PM7_MENU_INIT__) {
    window.__PM7_MENU_INIT__ = true;
    
    const initMenus = () => {
      // Regular initialization
      const menus = document.querySelectorAll('[data-pm7-menu]:not([data-pm7-menu-initialized])');
      menus.forEach((menu) => {
        try {
          new PM7Menu(menu);
        } catch (error) {
          console.error('[PM7Menu] Error initializing menu:', error);
        }
      });
    };
    
    // Initialize immediately if DOM is ready
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', initMenus, { once: true });
    } else {
      setTimeout(initMenus, 0);
    }
  }

  /**
   * PM7Dialog - Vanilla JavaScript dialog/modal component
   * Handles modal dialogs with accessibility features
   */
  class PM7Dialog {
    constructor(element) {
      this.element = element;
      
      // AI-Agent FIRST: Automatically add pm7-dialog class if missing
      if (!this.element.classList.contains('pm7-dialog')) {
        this.element.classList.add('pm7-dialog');
      }
      
      this.backdrop = element.querySelector('.pm7-dialog-overlay');
      this.closeButton = element.querySelector('.pm7-dialog-close');
      this.isOpen = false;
      this.previousActiveElement = null;
      this.focusableElements = [];
      
      this.init();
    }
    
    init() {
      // Close button
      if (this.closeButton) {
        this.closeButton.addEventListener('click', () => this.close());
      }
      
      // Backdrop click
      if (this.backdrop) {
        this.backdrop.addEventListener('click', (e) => {
          if (e.target === this.backdrop) {
            this.close();
          }
        });
      }
      
      // Escape key - use bound function to allow proper removal
      this.handleEscape = (e) => {
        if (e.key === 'Escape' && this.isOpen) {
          e.stopImmediatePropagation(); // Prevent other escape handlers
          this.close();
        }
      };
      
      // Tab trap - use bound function to allow proper removal
      this.handleTab = (e) => {
        if (e.key === 'Tab' && this.isOpen) {
          this.trapFocus(e);
        }
      };
    }
    
    open() {
      if (this.isOpen) return;
      
      // Close all open menus before opening dialog
      this.closeAllMenus();
      
      this.isOpen = true;
      this.previousActiveElement = document.activeElement;
      
      // Show dialog
      this.element.setAttribute('data-state', 'open');
      
      // Prevent body scroll
      document.body.classList.add('pm7-dialog-open');
      
      // Setup focus trap
      this.setupFocusTrap();
      
      // Add event listeners
      document.addEventListener('keydown', this.handleEscape);
      document.addEventListener('keydown', this.handleTab);
      
      // Focus first focusable element or close button
      requestAnimationFrame(() => {
        const firstFocusable = this.focusableElements[0];
        if (firstFocusable) {
          firstFocusable.focus();
        } else if (this.closeButton) {
          this.closeButton.focus();
        }
      });
      
      // Dispatch open event
      this.element.dispatchEvent(new CustomEvent('pm7-dialog-open', {
        detail: { dialog: this },
        bubbles: true
      }));
    }
    
    close() {
      if (!this.isOpen) return;
      
      this.isOpen = false;
      
      // Hide dialog
      this.element.setAttribute('data-state', 'closed');
      
      // Restore body scroll
      document.body.classList.remove('pm7-dialog-open');
      
      // Remove event listeners
      document.removeEventListener('keydown', this.handleEscape);
      document.removeEventListener('keydown', this.handleTab);
      
      // Restore focus
      if (this.previousActiveElement) {
        this.previousActiveElement.focus();
      }
      
      // Dispatch close event
      this.element.dispatchEvent(new CustomEvent('pm7-dialog-close', {
        detail: { dialog: this },
        bubbles: true
      }));
    }
    
    setupFocusTrap() {
      // Find all focusable elements
      const selector = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])';
      this.focusableElements = Array.from(this.element.querySelectorAll(selector))
        .filter(el => !el.disabled && el.offsetParent !== null);
    }
    
    trapFocus(e) {
      if (this.focusableElements.length === 0) return;
      
      const firstFocusable = this.focusableElements[0];
      const lastFocusable = this.focusableElements[this.focusableElements.length - 1];
      
      if (e.shiftKey) {
        // Shift + Tab
        if (document.activeElement === firstFocusable) {
          e.preventDefault();
          lastFocusable.focus();
        }
      } else {
        // Tab
        if (document.activeElement === lastFocusable) {
          e.preventDefault();
          firstFocusable.focus();
        }
      }
    }
    
    closeAllMenus() {
      // Close all open menus
      const openMenus = document.querySelectorAll('.pm7-menu-content--open, .pm7-menu-content[data-state="open"]');
      openMenus.forEach(menu => {
        menu.classList.remove('pm7-menu-content--open');
        menu.removeAttribute('data-state');
        
        // Update trigger state
        const menuContainer = menu.closest('.pm7-menu');
        if (menuContainer) {
          const trigger = menuContainer.querySelector('.pm7-menu-trigger');
          if (trigger) {
            trigger.setAttribute('aria-expanded', 'false');
          }
        }
      });
      
      // Also try using PM7Menu instances if available
      if (typeof window !== 'undefined' && window.PM7?.Menu) {
        // Access the static instances Map from the Menu class
        const MenuClass = window.PM7.Menu;
        if (MenuClass.instances && MenuClass.instances.forEach) {
          MenuClass.instances.forEach((instance) => {
            if (instance.isOpen) {
              instance.close();
            }
          });
        }
      }
    }
    
    shake() {
      this.element.classList.add('pm7-dialog--shake');
      setTimeout(() => {
        this.element.classList.remove('pm7-dialog--shake');
      }, 200);
    }
    
    setLoading(loading) {
      if (loading) {
        this.element.classList.add('pm7-dialog--loading');
      } else {
        this.element.classList.remove('pm7-dialog--loading');
      }
    }
  }

  // Helper function to create dialogs programmatically
  function createDialog(options = {}) {
    const {
      title = 'Dialog',
      content = '',
      size = 'md',
      variant = '',
      showClose = true,
      buttons = []
    } = options;
    
    // Create backdrop
    const overlay = document.createElement('div');
    overlay.className = 'pm7-dialog-overlay';
    
    // Create dialog
    const dialog = document.createElement('div');
    dialog.className = `pm7-dialog pm7-dialog--${size}`;
    if (variant) {
      dialog.className += ` pm7-dialog--${variant}`;
    }
    
    // Create header
    const header = document.createElement('div');
    header.className = 'pm7-dialog-header';
    
    const titleEl = document.createElement('h2');
    titleEl.className = 'pm7-dialog-title';
    titleEl.textContent = title;
    header.appendChild(titleEl);
    
    if (showClose) {
      const closeBtn = document.createElement('button');
      closeBtn.className = 'pm7-dialog-close';
      closeBtn.innerHTML = '×';
      closeBtn.setAttribute('aria-label', 'Close dialog');
      header.appendChild(closeBtn);
    }
    
    dialog.appendChild(header);
    
    // Create body
    const body = document.createElement('div');
    body.className = 'pm7-dialog-body';
    if (typeof content === 'string') {
      body.innerHTML = content;
    } else {
      body.appendChild(content);
    }
    dialog.appendChild(body);
    
    // Create footer if buttons provided
    if (buttons.length > 0) {
      const footer = document.createElement('div');
      footer.className = 'pm7-dialog-footer';
      
      buttons.forEach(btnOptions => {
        const btn = document.createElement('button');
        btn.className = `pm7-button pm7-button--${btnOptions.variant || 'primary'}`;
        btn.textContent = btnOptions.text;
        if (btnOptions.onClick) {
          btn.addEventListener('click', btnOptions.onClick);
        }
        footer.appendChild(btn);
      });
      
      dialog.appendChild(footer);
    }
    
    // Create container
    const container = document.createElement('div');
    container.appendChild(overlay);
    container.appendChild(dialog);
    
    // Add to body
    document.body.appendChild(container);
    
    // Initialize
    const dialogInstance = new PM7Dialog(container);
    
    // Clean up on close
    container.addEventListener('pm7-dialog-close', () => {
      setTimeout(() => {
        document.body.removeChild(container);
      }, 300);
    });
    
    return dialogInstance;
  }

  // Confirm dialog helper
  function pm7Confirm(message, options = {}) {
    return new Promise((resolve) => {
      const dialog = createDialog({
        title: options.title || 'Confirm',
        content: message,
        size: 'sm',
        buttons: [
          {
            text: options.cancelText || 'Cancel',
            variant: 'outline',
            onClick: () => {
              dialog.close();
              resolve(false);
            }
          },
          {
            text: options.confirmText || 'Confirm',
            variant: options.variant || 'primary',
            onClick: () => {
              dialog.close();
              resolve(true);
            }
          }
        ]
      });
      
      dialog.open();
    });
  }

  // Alert dialog helper
  function pm7Alert(message, options = {}) {
    return new Promise((resolve) => {
      const dialog = createDialog({
        title: options.title || 'Alert',
        content: message,
        size: 'sm',
        variant: options.variant,
        buttons: [
          {
            text: options.buttonText || 'OK',
            variant: 'primary',
            onClick: () => {
              dialog.close();
              resolve();
            }
          }
        ]
      });
      
      dialog.open();
    });
  }

  // Store ESC handlers to properly clean them up
  const escHandlers = new Map();

  // Helper function to close all open menus
  function closeAllOpenMenus() {
    // First try to close menus by removing open classes
    const openMenus = document.querySelectorAll('.pm7-menu-content--open, .pm7-menu-content[data-state="open"]');
    openMenus.forEach(menu => {
      menu.classList.remove('pm7-menu-content--open');
      menu.removeAttribute('data-state');
      
      // Update trigger state
      const menuContainer = menu.closest('.pm7-menu');
      if (menuContainer) {
        const trigger = menuContainer.querySelector('.pm7-menu-trigger');
        if (trigger) {
          trigger.setAttribute('aria-expanded', 'false');
        }
      }
    });
    
    // Also try using PM7Menu instances if available
    if (typeof window !== 'undefined' && window.PM7?.Menu) {
      // Access the static instances Map from the Menu class
      const MenuClass = window.PM7.Menu;
      if (MenuClass.instances && MenuClass.instances.forEach) {
        MenuClass.instances.forEach((instance) => {
          if (instance.isOpen) {
            instance.close();
          }
        });
      }
    }
  }

  // Predefined icons
  const dialogIcons = {
    info: `<svg class="pm7-dialog-icon-svg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" style="color: rgb(28, 134, 239);">
    <path d="M3 12a9 9 0 1 0 18 0 9 9 0 0 0-18 0m9-3h.01"></path>
    <path d="M11 12h1v4h1"></path>
  </svg>`,
    warning: `<svg class="pm7-dialog-icon-svg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" style="color: rgb(245, 158, 11);">
    <path d="M12 9v4m0 4h.01M5.07 19H19a2 2 0 0 0 1.75-2.95L13.75 4a2 2 0 0 0-3.5 0L3.25 16.05A2 2 0 0 0 5.07 19z"></path>
  </svg>`,
    error: `<svg class="pm7-dialog-icon-svg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" style="color: rgb(239, 68, 68);">
    <circle cx="12" cy="12" r="10"></circle>
    <line x1="15" y1="9" x2="9" y2="15"></line>
    <line x1="9" y1="9" x2="15" y2="15"></line>
  </svg>`,
    success: `<svg class="pm7-dialog-icon-svg" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="2" style="color: rgb(34, 197, 94);">
    <circle cx="12" cy="12" r="10"></circle>
    <path d="M9 12l2 2 4-4"></path>
  </svg>`
  };

  // Transform dialog based on content markers
  function transformDialog(dialogElement) {
    // Check if already transformed
    if (dialogElement.querySelector('.pm7-dialog-overlay')) {
      return;
    }

    // Read dialog attributes
    dialogElement.getAttribute('data-pm7-dialog');
    const size = dialogElement.getAttribute('data-pm7-dialog-size') || 'md';
    const showCloseButton = dialogElement.hasAttribute('data-pm7-show-close');
    // Default behavior: ESC and overlay close are enabled unless explicitly disabled
    const preventEscapeClose = dialogElement.hasAttribute('data-pm7-no-escape');
    const preventOverlayClose = dialogElement.hasAttribute('data-pm7-no-overlay-close');

    // Get content sections - CRITICAL: Read ALL content before clearing!
    const headerEl = dialogElement.querySelector('[data-pm7-header]');
    const bodyEl = dialogElement.querySelector('[data-pm7-body]');
    const footerEl = dialogElement.querySelector('[data-pm7-footer]');

    // Store section data IMMEDIATELY before anything can modify the DOM
    const sections = {
      header: headerEl ? {
        content: headerEl.innerHTML,
        title: headerEl.getAttribute('data-pm7-dialog-title'),
        subtitle: headerEl.getAttribute('data-pm7-dialog-subtitle'),
        icon: headerEl.getAttribute('data-pm7-dialog-icon'),
        separator: headerEl.hasAttribute('data-pm7-header-separator')
      } : null,
      body: bodyEl ? bodyEl.innerHTML : null,
      footer: footerEl ? footerEl.innerHTML : null
    };

    // NOW clear dialog - AFTER we've safely stored all content
    dialogElement.innerHTML = '';
    
    // Add pm7-dialog class if missing
    if (!dialogElement.classList.contains('pm7-dialog')) {
      dialogElement.classList.add('pm7-dialog');
    }

    // Create overlay
    const overlay = document.createElement('div');
    overlay.className = 'pm7-dialog-overlay';
    dialogElement.appendChild(overlay);

    // Create content container
    const content = document.createElement('div');
    content.className = `pm7-dialog-content pm7-dialog-content--${size}`;

    // Build header if exists
    if (sections.header) {
      const header = document.createElement('div');
      header.className = 'pm7-dialog-header';

      // Create a container for title and subtitle
      const textContainer = document.createElement('div');
      textContainer.className = 'pm7-dialog-header-text';

      // Add title if specified
      if (sections.header.title) {
        const titleEl = document.createElement('h2');
        titleEl.className = 'pm7-dialog-title';
        titleEl.textContent = sections.header.title;
        textContainer.appendChild(titleEl);
      }

      // Add subtitle if specified
      if (sections.header.subtitle) {
        const subtitleEl = document.createElement('p');
        subtitleEl.className = 'pm7-dialog-description';
        subtitleEl.textContent = sections.header.subtitle;
        textContainer.appendChild(subtitleEl);
      }

      header.appendChild(textContainer);

      // Create a container for actions (icon and close button)
      const actionsContainer = document.createElement('div');
      actionsContainer.className = 'pm7-dialog-header-actions';

      // Add icon if specified
      if (sections.header.icon) {
        const iconDiv = document.createElement('div');
        iconDiv.className = 'pm7-dialog-icon';
        iconDiv.innerHTML = dialogIcons[sections.header.icon] || '';
        actionsContainer.appendChild(iconDiv);
      }

      // Add close button if requested
      if (showCloseButton) {
        const closeBtn = document.createElement('button');
        closeBtn.className = 'pm7-dialog-close';
        closeBtn.setAttribute('aria-label', 'Close');
        closeBtn.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <line x1="18" y1="6" x2="6" y2="18"/>
        <line x1="6" y1="6" x2="18" y2="18"/>
      </svg>`;
        actionsContainer.appendChild(closeBtn);
      }

      header.appendChild(actionsContainer);

      content.appendChild(header);

      // Add header separator if requested
      if (sections.header.separator) {
        const separator = document.createElement('div');
        separator.className = 'pm7-dialog-header-separator';
        content.appendChild(separator);
      }
    }

    // Add body if exists
    if (sections.body !== null) {
      const body = document.createElement('div');
      body.className = 'pm7-dialog-body';
      body.innerHTML = sections.body;
      content.appendChild(body);
    }

    // Add footer if exists
    if (sections.footer !== null) {
      const footer = document.createElement('div');
      footer.className = 'pm7-dialog-footer';
      footer.innerHTML = sections.footer;
      content.appendChild(footer);
    }

    dialogElement.appendChild(content);

    // Store dialog settings for openDialog function
    dialogElement._dialogSettings = {
      closeOnEscape: !preventEscapeClose,  // Inverted: true by default
      closeOnOverlay: !preventOverlayClose  // Inverted: true by default
    };
    
    // Set initial state
    dialogElement.setAttribute('data-state', 'closed');
  }

  // Simple helper functions for pm7-dialog elements
  function openDialog(dialogId) {
    const dialogElement = document.querySelector(`[data-pm7-dialog="${dialogId}"]`);
    if (!dialogElement) {
      console.warn(`Dialog with id "${dialogId}" not found`);
      return;
    }
    
    // Check if dialog needs transformation
    const needsTransform = !dialogElement.querySelector('.pm7-dialog-overlay');
    
    // Self-healing: detect if framework re-rendered the original structure
    const hasOriginalMarkers = 
      dialogElement.querySelector('[data-pm7-header]') ||
      dialogElement.querySelector('[data-pm7-body]') ||
      dialogElement.querySelector('[data-pm7-footer]');
    
    if (hasOriginalMarkers && needsTransform) {
      // Dialog structure was restored by framework re-render
      const currentState = dialogElement.getAttribute('data-state');
      
      // Only re-initialize if not currently open or closing
      if (currentState !== 'open' && currentState !== 'closing') {
        // Transform the dialog
        transformDialog(dialogElement);
        // Continue with normal open flow after transformation
      } else if (currentState === 'open') {
        // Dialog is already open, don't re-open
        return;
      } else if (currentState === 'closing') {
        // Dialog is closing, don't interfere
        return;
      }
    } else if (needsTransform) {
      // No markers but needs transform (shouldn't happen in normal flow)
      transformDialog(dialogElement);
    }
    
    // Check if already open (prevent double-open)
    if (dialogElement.getAttribute('data-state') === 'open') {
      return;
    }
    
    // Check if closing (prevent open during close animation)
    if (dialogElement.getAttribute('data-state') === 'closing') {
      return;
    }
    
    // Close all open menus before opening dialog
    closeAllOpenMenus();
    
    // Set dialog to open state
    dialogElement.setAttribute('data-state', 'open');
    document.body.classList.add('pm7-dialog-open');
    
    // Get dialog settings
    const settings = dialogElement._dialogSettings || {};
    
    // Add close handlers
    const overlay = dialogElement.querySelector('.pm7-dialog-overlay');
    const closeBtn = dialogElement.querySelector('.pm7-dialog-close');
    
    // Overlay click handler - enabled by default unless settings.closeOnOverlay is false
    if (overlay && settings.closeOnOverlay !== false) {
      overlay.onclick = () => closeDialog(dialogId);
    }
    
    if (closeBtn) {
      closeBtn.onclick = () => closeDialog(dialogId);
    }
    
    // ESC key handler - store it so we can remove it later
    if (settings.closeOnEscape !== false) {  // Default true unless explicitly false
      const escHandler = (e) => {
        if (e.key === 'Escape') {
          closeDialog(dialogId);
        }
      };
      
      // Remove any existing handler for this dialog
      if (escHandlers.has(dialogId)) {
        document.removeEventListener('keydown', escHandlers.get(dialogId));
      }
      
      // Store and add new handler
      escHandlers.set(dialogId, escHandler);
      document.addEventListener('keydown', escHandler);
    }
  }

  function closeDialog(dialogId) {
    const dialogElement = document.querySelector(`[data-pm7-dialog="${dialogId}"]`);
    if (!dialogElement) return;
    
    // Add closing state for animation
    dialogElement.setAttribute('data-state', 'closing');
    dialogElement.classList.remove('pm7-dialog--open');
    
    // Wait for animation to complete before removing
    setTimeout(() => {
      dialogElement.setAttribute('data-state', 'closed');
      
      // Check if any other dialogs are open
      const openDialogs = document.querySelectorAll('.pm7-dialog[data-state="open"]');
      if (openDialogs.length === 0) {
        document.body.classList.remove('pm7-dialog-open');
      }
    }, 200); // Match transition duration
    
    // Remove ESC handler for this dialog
    if (escHandlers.has(dialogId)) {
      document.removeEventListener('keydown', escHandlers.get(dialogId));
      escHandlers.delete(dialogId);
    }
  }

  // Auto-initialize dialogs
  function autoInitDialogs() {
    const dialogs = document.querySelectorAll('[data-pm7-dialog]:not([data-state])');
    dialogs.forEach(dialog => {
      // Transform dialog structure if needed
      transformDialog(dialog);
      // Set initial closed state
      dialog.setAttribute('data-state', 'closed');
    });
  }

  // Initialize on DOM ready for traditional apps
  if (typeof document !== 'undefined') {
    document.addEventListener('DOMContentLoaded', autoInitDialogs);
  }

  // Make openDialog and closeDialog available globally for convenience
  if (typeof window !== 'undefined') {
    window.openDialog = openDialog;
    window.closeDialog = closeDialog;
  }

  // Don't automatically pollute global scope
  // These functions are available via window.PM7 namespace

  /**
   * PM7 Button Component JavaScript
   * Adds 6stars effect to primary buttons
   */

  class PM7Button {
    constructor(element) {
      this.element = element;
      this.init();
    }

    init() {
      // Only add 6stars to primary buttons
      if (this.element.classList.contains('pm7-button--primary') || 
          this.element.classList.contains('pm7-button--default')) {
        this.add6StarsEffect();
      }
      
      // Initialize slider button functionality
      if (this.element.classList.contains('pm7-button--slider')) {
        this.initSlider();
      }
    }

    add6StarsEffect() {
      // Create 6stars container
      const starsContainer = document.createElement('div');
      starsContainer.className = 'pm7-button__6stars';

      // Create 6 stars
      for (let i = 0; i < 6; i++) {
        const star = document.createElement('span');
        star.className = 'star';
        starsContainer.appendChild(star);
      }

      // Add to button
      this.element.appendChild(starsContainer);
    }
    
    initSlider() {
      this.handle = this.element.querySelector('.pm7-button--slider-handle');
      this.text = this.element.querySelector('.pm7-button--slider-text');
      
      if (!this.handle) return;
      
      this.isDragging = false;
      this.startX = 0;
      this.currentX = 0;
      this.handleX = 0;
      this.maxX = 0;
      this.threshold = 0.95; // 95% to complete
      
      // Bind event handlers
      this.handleMouseDown = this.handleMouseDown.bind(this);
      this.handleMouseMove = this.handleMouseMove.bind(this);
      this.handleMouseUp = this.handleMouseUp.bind(this);
      this.handleTouchStart = this.handleTouchStart.bind(this);
      this.handleTouchMove = this.handleTouchMove.bind(this);
      this.handleTouchEnd = this.handleTouchEnd.bind(this);
      
      // Add event listeners
      this.handle.addEventListener('mousedown', this.handleMouseDown);
      this.handle.addEventListener('touchstart', this.handleTouchStart, { passive: false });
      
      // Calculate max position
      this.updateMaxPosition();
      
      // Update on resize
      window.addEventListener('resize', () => this.updateMaxPosition());
    }
    
    updateMaxPosition() {
      const buttonWidth = this.element.offsetWidth;
      const handleWidth = this.handle.offsetWidth;
      this.maxX = buttonWidth - handleWidth - 8; // 4px padding on each side
    }
    
    handleMouseDown(e) {
      if (this.element.disabled || this.element.hasAttribute('data-pm7-slider-complete')) return;
      
      this.isDragging = true;
      this.startX = e.clientX - this.handleX;
      this.element.setAttribute('data-pm7-slider-dragging', 'true');
      
      document.addEventListener('mousemove', this.handleMouseMove);
      document.addEventListener('mouseup', this.handleMouseUp);
      
      e.preventDefault();
    }
    
    handleTouchStart(e) {
      if (this.element.disabled || this.element.hasAttribute('data-pm7-slider-complete')) return;
      
      const touch = e.touches[0];
      this.isDragging = true;
      this.startX = touch.clientX - this.handleX;
      this.element.setAttribute('data-pm7-slider-dragging', 'true');
      
      document.addEventListener('touchmove', this.handleTouchMove, { passive: false });
      document.addEventListener('touchend', this.handleTouchEnd);
      
      e.preventDefault();
    }
    
    handleMouseMove(e) {
      if (!this.isDragging) return;
      
      this.currentX = e.clientX - this.startX;
      this.updateHandlePosition();
    }
    
    handleTouchMove(e) {
      if (!this.isDragging) return;
      
      const touch = e.touches[0];
      this.currentX = touch.clientX - this.startX;
      this.updateHandlePosition();
      
      e.preventDefault();
    }
    
    updateHandlePosition() {
      // Constrain position
      this.handleX = Math.max(0, Math.min(this.currentX, this.maxX));
      
      // Update handle position
      this.handle.style.transform = `translateX(${this.handleX}px)`;
      
      // Check if threshold reached
      const progress = this.handleX / this.maxX;
      if (progress >= this.threshold) {
        this.complete();
      }
    }
    
    handleMouseUp() {
      this.endDrag();
      document.removeEventListener('mousemove', this.handleMouseMove);
      document.removeEventListener('mouseup', this.handleMouseUp);
    }
    
    handleTouchEnd() {
      this.endDrag();
      document.removeEventListener('touchmove', this.handleTouchMove);
      document.removeEventListener('touchend', this.handleTouchEnd);
    }
    
    endDrag() {
      if (!this.isDragging) return;
      
      this.isDragging = false;
      this.element.removeAttribute('data-pm7-slider-dragging');
      
      // If not completed, snap back
      const progress = this.handleX / this.maxX;
      if (progress < this.threshold && !this.element.hasAttribute('data-pm7-slider-complete')) {
        this.handleX = 0;
        this.handle.style.transform = 'translateX(0)';
      }
    }
    
    complete() {
      if (this.element.hasAttribute('data-pm7-slider-complete')) return;
      
      // Snap to end
      this.handleX = this.maxX;
      this.handle.style.transform = `translateX(${this.maxX}px)`;
      
      // Mark as complete
      this.element.setAttribute('data-pm7-slider-complete', 'true');
      
      // Dispatch event
      this.element.dispatchEvent(new CustomEvent('pm7:slider:complete', {
        bubbles: true,
        detail: { button: this.element }
      }));
      
      // Trigger click event after a small delay
      setTimeout(() => {
        this.element.click();
      }, 300);
    }
    
    reset() {
      this.handleX = 0;
      this.handle.style.transform = 'translateX(0)';
      this.element.removeAttribute('data-pm7-slider-complete');
      this.element.removeAttribute('data-pm7-slider-dragging');
    }
  }

  // Auto-initialize buttons
  function initButtons() {
    // Initialize primary/default buttons with 6stars
    document.querySelectorAll('.pm7-button--primary, .pm7-button--default').forEach(button => {
      if (!button.querySelector('.pm7-button__6stars')) {
        new PM7Button(button);
      }
    });
    
    // Initialize slider buttons
    document.querySelectorAll('.pm7-button--slider').forEach(button => {
      if (!button.PM7Button) {
        button.PM7Button = new PM7Button(button);
      }
    });
  }

  // Initialize on DOM ready
  if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', initButtons);
    } else {
      initButtons();
    }
  }

  /**
   * PM7 Toast Component JavaScript
   * Provides toast notification functionality
   */

  class PM7Toast {
    constructor() {
      this.viewport = null;
      this.toasts = new Map();
      this.init();
    }

    init() {
      // Create viewport if it doesn't exist
      if (!document.querySelector('.pm7-toast-viewport')) {
        this.viewport = document.createElement('div');
        this.viewport.className = 'pm7-toast-viewport';
        document.body.appendChild(this.viewport);
      } else {
        this.viewport = document.querySelector('.pm7-toast-viewport');
      }
    }

    show(options = {}) {
      const {
        title = '',
        description = '',
        variant = 'default',
        duration = 5000,
        action = null,
        onClose = null
      } = options;

      // Create toast element
      const toast = document.createElement('div');
      const id = Date.now().toString();
      toast.className = `pm7-toast pm7-toast--${variant}`;
      toast.setAttribute('data-state', 'open');
      toast.setAttribute('data-toast-id', id);

      // Build toast content
      const toastHeader = document.createElement('div');
      toastHeader.className = 'pm7-toast-header';

      const textContainer = document.createElement('div');
      if (title) {
        const titleEl = document.createElement('h3');
        titleEl.className = 'pm7-toast-title';
        titleEl.textContent = title;
        textContainer.appendChild(titleEl);
      }
      if (description) {
        const descriptionEl = document.createElement('p');
        descriptionEl.className = 'pm7-toast-description';
        descriptionEl.textContent = description;
        textContainer.appendChild(descriptionEl);
      }
      toastHeader.appendChild(textContainer);

      const closeButton = document.createElement('button');
      closeButton.className = 'pm7-toast-close';
      closeButton.setAttribute('aria-label', 'Close');
      closeButton.innerHTML = '&times;';
      toastHeader.appendChild(closeButton);

      toast.appendChild(toastHeader);

      if (action) {
        const actionContainer = document.createElement('div');
        actionContainer.className = 'pm7-toast-action';
        actionContainer.innerHTML = action;
        toast.appendChild(actionContainer);
      }

      if (duration > 0) {
        const progressBar = document.createElement('div');
        progressBar.className = 'pm7-toast-progress';
        progressBar.style.animationDuration = `${duration}ms`;
        toast.appendChild(progressBar);
      }

      // Add close handler
      closeButton.addEventListener('click', () => this.close(id));

      // Add to viewport
      this.viewport.appendChild(toast);
      this.toasts.set(id, { element: toast, onClose });

      // Auto-dismiss
      if (duration > 0) {
        setTimeout(() => this.close(id), duration);
      }

      return id;
    }

    close(id) {
      const toast = this.toasts.get(id);
      if (!toast) return;

      const { element, onClose } = toast;
      
      // Trigger closing animation
      element.setAttribute('data-state', 'closed');

      // Remove after animation
      setTimeout(() => {
        element.remove();
        this.toasts.delete(id);
        if (onClose) onClose();
      }, 200);
    }

    closeAll() {
      this.toasts.forEach((_, id) => this.close(id));
    }
  }

  // Create global instance
  let globalToast = null;

  // Helper functions
  function showToast(options) {
    if (!globalToast) {
      globalToast = new PM7Toast();
    }
    return globalToast.show(options);
  }

  function closeToast(id) {
    if (globalToast) {
      globalToast.close(id);
    }
  }

  function closeAllToasts() {
    if (globalToast) {
      globalToast.closeAll();
    }
  }

  // Remove auto-initialization - toast will be created on first use

  /**
   * PM7TabSelector - Tab selector component with self-healing
   * Handles tab navigation with automatic recovery from framework re-renders
   */
  class PM7TabSelector {
    static instances = new WeakMap();
    
    constructor(element) {
      // Self-healing: Check if element was re-rendered by framework
      const wasInitialized = element.hasAttribute('data-pm7-tab-initialized');
      const hasInstance = PM7TabSelector.instances.has(element);
      
      // If initialized but no instance, element was re-rendered
      if (wasInitialized && !hasInstance) {
        console.log('[PM7TabSelector] Self-healing: Re-initializing tabs after framework re-render');
        element.removeAttribute('data-pm7-tab-initialized');
      }
      
      // Check if this element already has a tab selector instance
      if (PM7TabSelector.instances.has(element)) {
        return PM7TabSelector.instances.get(element);
      }
      
      this.element = element;
      
      // Preserve state if this is a re-render
      const preservedState = this.preserveState();
      
      // AI-Agent FIRST: Automatically add pm7-tab-selector class if missing
      if (!this.element.classList.contains('pm7-tab-selector')) {
        this.element.classList.add('pm7-tab-selector');
      }
      
      this.tabList = element.querySelector('.pm7-tab-list');
      // Only get direct child tabs and panels, not nested ones
      this.tabs = Array.from(element.querySelectorAll('.pm7-tab-trigger')).filter(tab => 
        tab.closest('[data-pm7-tab-selector]') === element
      );
      this.panels = Array.from(element.querySelectorAll('.pm7-tab-content')).filter(panel => 
        panel.closest('[data-pm7-tab-selector]') === element
      );
      this.activeTab = null;
      this.eventListeners = new Map();
      
      if (!this.tabList || this.tabs.length === 0) {
        console.warn('PM7TabSelector: Missing required elements');
        return;
      }
      
      // Store this instance
      PM7TabSelector.instances.set(element, this);
      
      // Store instance reference on element for self-healing
      element._pm7TabSelectorInstance = this;
      
      this.init();
      
      // Restore state if this was a re-render
      if (preservedState && preservedState.activeTabIndex !== -1) {
        this.restoreState(preservedState);
      }
      
      // Mark as initialized
      element.setAttribute('data-pm7-tab-initialized', 'true');
    }
    
    preserveState() {
      // Find currently active tab
      const activeTab = this.element.querySelector('.pm7-tab-trigger[data-state="active"], .pm7-tab-trigger--active');
      const tabs = Array.from(this.element.querySelectorAll('.pm7-tab-trigger')).filter(tab => 
        tab.closest('[data-pm7-tab-selector]') === this.element
      );
      
      const activeTabIndex = activeTab ? tabs.indexOf(activeTab) : -1;
      
      return {
        activeTabIndex,
        tabListId: this.element.querySelector('.pm7-tab-list')?.id
      };
    }
    
    restoreState(state) {
      if (state.activeTabIndex !== -1 && state.activeTabIndex < this.tabs.length) {
        // Use setTimeout to ensure DOM is ready
        setTimeout(() => {
          this.selectTab(this.tabs[state.activeTabIndex]);
        }, 0);
      }
    }
    
    cleanup() {
      // Remove all event listeners
      this.eventListeners.forEach(({ element, type, handler }) => {
        element.removeEventListener(type, handler);
      });
      this.eventListeners.clear();
    }
    
    destroy() {
      this.cleanup();
      PM7TabSelector.instances.delete(this.element);
      delete this.element._pm7TabSelectorInstance;
    }
    
    init() {
      // Remove any existing event listeners to prevent duplicates
      this.cleanup();
      
      // Set up ARIA attributes
      this.tabList.setAttribute('role', 'tablist');
      
      this.tabs.forEach((tab, index) => {
        const panel = this.panels[index];
        let panelId = tab.getAttribute('aria-controls');
        
        // If no aria-controls, use panel's existing id or generate new one
        if (!panelId) {
          panelId = panel?.id || `pm7-tab-panel-${Math.random().toString(36).substr(2, 9)}`;
          tab.setAttribute('aria-controls', panelId);
        }
        
        // Set up tab
        tab.setAttribute('role', 'tab');
        tab.setAttribute('tabindex', '-1');
        
        // Set up panel
        if (panel) {
          panel.setAttribute('role', 'tabpanel');
          if (!panel.id) {
            panel.setAttribute('id', panelId);
          }
          panel.setAttribute('aria-labelledby', tab.id || (tab.id = `pm7-tab-${Math.random().toString(36).substr(2, 9)}`));
          panel.setAttribute('tabindex', '0');
        }
        
        // Create bound handlers for cleanup
        const clickHandler = () => this.selectTab(tab);
        const keyHandler = (e) => this.handleKeyDown(e);
        
        // Add event listeners
        tab.addEventListener('click', clickHandler);
        tab.addEventListener('keydown', keyHandler);
        
        // Store listeners for cleanup
        this.eventListeners.set(`click-${index}`, { element: tab, type: 'click', handler: clickHandler });
        this.eventListeners.set(`keydown-${index}`, { element: tab, type: 'keydown', handler: keyHandler });
      });
      
      // Activate first tab or already active tab
      const initialActiveTab = this.tabs.find(tab =>
        tab.getAttribute('data-state') === 'active' ||
        tab.classList.contains('pm7-tab-trigger--active')
      ) || this.tabs[0];

      this.selectTab(initialActiveTab);
      // Ensure the initial active tab has tabindex 0
      if (initialActiveTab) {
        initialActiveTab.setAttribute('tabindex', '0');
      }
    }
    
    selectTab(tab) {
      if (!tab || tab.disabled || tab === this.activeTab) return;
      
      const tabIndex = this.tabs.indexOf(tab);
      const panel = this.panels[tabIndex];
      
      // Deactivate all tabs
      this.tabs.forEach((t, i) => {
        t.setAttribute('data-state', 'inactive');
        t.setAttribute('aria-selected', 'false');
        t.setAttribute('tabindex', '-1');
        t.classList.remove('pm7-tab-trigger--active');
        
        const p = this.panels[i];
        if (p) {
          p.setAttribute('data-state', 'inactive');
          p.classList.remove('pm7-tab-content--active');
        }
      });
      
      // Activate selected tab
      tab.setAttribute('data-state', 'active');
      tab.setAttribute('aria-selected', 'true');
      tab.setAttribute('tabindex', '0');
      tab.classList.add('pm7-tab-trigger--active');
      
      if (panel) {
        panel.setAttribute('data-state', 'active');
        panel.classList.add('pm7-tab-content--active');
      }
      
      this.activeTab = tab;
      
      // Dispatch event
      const event = new CustomEvent('pm7-tab-change', {
        detail: { tab, panel, index: tabIndex },
        bubbles: true
      });
      this.element.dispatchEvent(event);
    }
    
    handleKeyDown(e) {
      const currentIndex = this.tabs.indexOf(e.target);
      let nextIndex = -1;
      
      switch (e.key) {
        case 'ArrowLeft':
        case 'ArrowUp':
          e.preventDefault();
          nextIndex = currentIndex - 1;
          if (nextIndex < 0) nextIndex = this.tabs.length - 1;
          break;
          
        case 'ArrowRight':
        case 'ArrowDown':
          e.preventDefault();
          nextIndex = currentIndex + 1;
          if (nextIndex >= this.tabs.length) nextIndex = 0;
          break;
          
        case 'Home':
          e.preventDefault();
          nextIndex = 0;
          break;
          
        case 'End':
          e.preventDefault();
          nextIndex = this.tabs.length - 1;
          break;
          
        case 'Enter':
        case ' ':
          e.preventDefault();
          this.selectTab(e.target);
          return;
      }
      
      if (nextIndex !== -1) {
        const nextTab = this.tabs[nextIndex];
        if (nextTab && !nextTab.disabled) {
          this.selectTab(nextTab);
          nextTab.focus();
        }
      }
    }
    
    // Public methods
    selectTabByIndex(index) {
      if (index >= 0 && index < this.tabs.length) {
        this.selectTab(this.tabs[index]);
      }
    }
    
    getActiveTabIndex() {
      return this.tabs.indexOf(this.activeTab);
    }
  }

  // Self-healing function
  function healTabSelectors$1() {
    // Find all tab selectors that were initialized but lost their instance
    const lostTabSelectors = document.querySelectorAll('[data-pm7-tab-selector][data-pm7-tab-initialized]:not([data-pm7-tab-healing])');
    
    lostTabSelectors.forEach(selector => {
      if (!selector._pm7TabSelectorInstance || !PM7TabSelector.instances.has(selector)) {
        selector.setAttribute('data-pm7-tab-healing', 'true');
        console.log('[PM7TabSelector] Healing tab selector:', selector);
        new PM7TabSelector(selector);
        selector.removeAttribute('data-pm7-tab-healing');
      }
    });
  }

  // Auto-initialize tab selectors
  function initTabSelectors() {
    const selectors = document.querySelectorAll('[data-pm7-tab-selector]:not([data-pm7-tab-initialized])');
    selectors.forEach(selector => {
      new PM7TabSelector(selector);
    });
  }

  // Auto-initialize on DOM ready
  if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', initTabSelectors, { once: true });
    } else {
      setTimeout(initTabSelectors, 0);
    }
  }

  // Make healing function available
  PM7TabSelector.heal = healTabSelectors$1;

  /**
   * PM7 Tooltip Component
   * Interactive tooltip functionality
   */

  class PM7Tooltip {
    static instances = new WeakMap();
    
    constructor(element) {
      // Self-healing: Check if element was re-rendered by framework
      const wasInitialized = element.hasAttribute('data-pm7-tooltip-initialized');
      const hasInstance = PM7Tooltip.instances.has(element);
      
      // If initialized but no instance, element was re-rendered
      if (wasInitialized && !hasInstance) {
        console.log('[PM7Tooltip] Self-healing: Re-initializing tooltip after framework re-render');
        element.removeAttribute('data-pm7-tooltip-initialized');
      }
      
      // Check if this element already has a tooltip instance
      if (PM7Tooltip.instances.has(element)) {
        return PM7Tooltip.instances.get(element);
      }
      
      this.element = element;
      
      // Only support structured syntax - no simple syntax transformation
      
      // AI-Agent FIRST: Automatically add pm7-tooltip class if missing
      if (!this.element.classList.contains('pm7-tooltip')) {
        this.element.classList.add('pm7-tooltip');
      }
      
      this.trigger = element.querySelector('.pm7-tooltip-trigger');
      this.content = element.querySelector('.pm7-tooltip-content');
      this.arrow = element.querySelector('.pm7-tooltip-arrow');
      
      // Configuration
      this.side = this.content?.dataset.side || 'top';
      this.align = this.content?.dataset.align || 'center';
      this.delay = parseInt(element.dataset.delay || '0');
      this.openDelay = parseInt(element.dataset.openDelay || this.delay || '0');
      this.closeDelay = parseInt(element.dataset.closeDelay || '0');
      
      // State
      this.isOpen = false;
      this.openTimeout = null;
      this.closeTimeout = null;
      this.eventListeners = new Map();
      
      // Preserve state if this is a re-render
      const preservedState = this.preserveState();
      
      // Store this instance
      PM7Tooltip.instances.set(element, this);
      
      // Store instance reference on element for self-healing
      element._pm7TooltipInstance = this;
      
      // Bind methods
      this.handleTriggerMouseEnter = this.handleTriggerMouseEnter.bind(this);
      this.handleTriggerMouseLeave = this.handleTriggerMouseLeave.bind(this);
      this.handleTriggerFocus = this.handleTriggerFocus.bind(this);
      this.handleTriggerBlur = this.handleTriggerBlur.bind(this);
      this.handleTriggerClick = this.handleTriggerClick.bind(this);
      this.handleDocumentClick = this.handleDocumentClick.bind(this);
      this.handleKeyDown = this.handleKeyDown.bind(this);
      this.updatePosition = this.updatePosition.bind(this);
      
      this.init();
      
      // Restore state if this was a re-render
      if (preservedState && preservedState.wasOpen) {
        this.restoreState(preservedState);
      }
      
      // Mark as initialized
      element.setAttribute('data-pm7-tooltip-initialized', 'true');
    }
    
    
    preserveState() {
      // Check if tooltip is currently open
      const content = this.element.querySelector('.pm7-tooltip-content');
      const wasOpen = content?.getAttribute('data-state') === 'open';
      
      return {
        wasOpen,
        side: content?.dataset.side,
        align: content?.dataset.align
      };
    }
    
    restoreState(state) {
      if (state.wasOpen) {
        // Restore open state after a brief delay
        setTimeout(() => {
          this.show();
        }, 50);
      }
    }
    
    cleanup() {
      // Remove all event listeners
      this.eventListeners.forEach(({ element, type, handler }) => {
        element.removeEventListener(type, handler);
      });
      this.eventListeners.clear();
      
      // Clear timeouts
      this.clearTimeouts();
    }
    
    init() {
      if (!this.trigger || !this.content) return;
      
      // Remove any existing event listeners to prevent duplicates
      this.cleanup();
      
      // Set initial ARIA attributes
      this.trigger.setAttribute('aria-describedby', this.content.id || this.generateId());
      this.content.setAttribute('role', 'tooltip');
      this.content.setAttribute('data-state', 'closed');
      
      // Add event listeners and track them
      this.trigger.addEventListener('mouseenter', this.handleTriggerMouseEnter);
      this.eventListeners.set('mouseenter', { element: this.trigger, type: 'mouseenter', handler: this.handleTriggerMouseEnter });
      
      this.trigger.addEventListener('mouseleave', this.handleTriggerMouseLeave);
      this.eventListeners.set('mouseleave', { element: this.trigger, type: 'mouseleave', handler: this.handleTriggerMouseLeave });
      
      this.trigger.addEventListener('focus', this.handleTriggerFocus);
      this.eventListeners.set('focus', { element: this.trigger, type: 'focus', handler: this.handleTriggerFocus });
      
      this.trigger.addEventListener('blur', this.handleTriggerBlur);
      this.eventListeners.set('blur', { element: this.trigger, type: 'blur', handler: this.handleTriggerBlur });
      
      // Touch support
      if ('ontouchstart' in window) {
        this.trigger.addEventListener('click', this.handleTriggerClick);
        this.eventListeners.set('click', { element: this.trigger, type: 'click', handler: this.handleTriggerClick });
      }
    }
    
    handleTriggerMouseEnter() {
      this.clearTimeouts();
      
      if (this.openDelay > 0) {
        this.openTimeout = setTimeout(() => {
          this.show();
        }, this.openDelay);
      } else {
        this.show();
      }
    }
    
    handleTriggerMouseLeave() {
      this.clearTimeouts();
      
      if (this.closeDelay > 0) {
        this.closeTimeout = setTimeout(() => {
          this.hide();
        }, this.closeDelay);
      } else {
        this.hide();
      }
    }
    
    handleTriggerFocus() {
      this.show();
    }
    
    handleTriggerBlur() {
      this.hide();
    }
    
    handleTriggerClick(event) {
      event.preventDefault();
      event.stopPropagation();
      
      if (this.isOpen) {
        this.hide();
      } else {
        this.show();
        // Add document listener for closing on outside click
        setTimeout(() => {
          document.addEventListener('click', this.handleDocumentClick);
        }, 0);
      }
    }
    
    handleDocumentClick(event) {
      if (!this.element.contains(event.target)) {
        this.hide();
        document.removeEventListener('click', this.handleDocumentClick);
      }
    }
    
    handleKeyDown(event) {
      if (event.key === 'Escape' && this.isOpen) {
        this.hide();
        this.trigger.focus();
      }
    }
    
    show() {
      if (this.isOpen) return;
      
      this.isOpen = true;
      this.content.setAttribute('data-state', 'open');
      
      // Update position before showing
      this.updatePosition();
      
      // Add escape key listener
      document.addEventListener('keydown', this.handleKeyDown);
      
      // Add scroll and resize listeners for fixed positioning
      window.addEventListener('scroll', this.updatePosition, true);
      window.addEventListener('resize', this.updatePosition);
      
      // Announce to screen readers
      this.announceToScreenReaders();
      
      // Dispatch custom event
      this.element.dispatchEvent(new CustomEvent('pm7:tooltip:show', {
        bubbles: true,
        detail: { tooltip: this }
      }));
    }
    
    hide() {
      if (!this.isOpen) return;
      
      this.isOpen = false;
      this.content.setAttribute('data-state', 'closed');
      
      // Remove escape key listener
      document.removeEventListener('keydown', this.handleKeyDown);
      
      // Remove scroll and resize listeners
      window.removeEventListener('scroll', this.updatePosition, true);
      window.removeEventListener('resize', this.updatePosition);
      
      // Dispatch custom event
      this.element.dispatchEvent(new CustomEvent('pm7:tooltip:hide', {
        bubbles: true,
        detail: { tooltip: this }
      }));
    }
    
    updatePosition() {
      if (!this.trigger || !this.content) return;
      
      // Reset position for measurement
      this.content.style.top = '';
      this.content.style.left = '';
      this.content.style.right = '';
      this.content.style.bottom = '';
      
      const triggerRect = this.trigger.getBoundingClientRect();
      const contentRect = this.content.getBoundingClientRect();
      const viewportWidth = window.innerWidth;
      const viewportHeight = window.innerHeight;
      
      // Check if tooltip fits in preferred position
      let actualSide = this.side;
      const padding = 8; // Minimum distance from viewport edge
      const gap = 8; // Gap between trigger and tooltip
      
      // Auto-adjust position if it doesn't fit
      if (actualSide === 'top' && triggerRect.top - contentRect.height - padding < 0) {
        actualSide = 'bottom';
      } else if (actualSide === 'bottom' && triggerRect.bottom + contentRect.height + padding > viewportHeight) {
        actualSide = 'top';
      } else if (actualSide === 'left' && triggerRect.left - contentRect.width - padding < 0) {
        actualSide = 'right';
      } else if (actualSide === 'right' && triggerRect.right + contentRect.width + padding > viewportWidth) {
        actualSide = 'left';
      }
      
      // Update data attribute for styling
      this.content.setAttribute('data-side', actualSide);
      
      // Calculate position based on side (now using fixed positioning)
      let top, left;
      
      switch (actualSide) {
        case 'top':
          top = triggerRect.top - contentRect.height - gap;
          left = triggerRect.left + (triggerRect.width - contentRect.width) / 2;
          break;
        case 'bottom':
          top = triggerRect.bottom + gap;
          left = triggerRect.left + (triggerRect.width - contentRect.width) / 2;
          break;
        case 'left':
          top = triggerRect.top + (triggerRect.height - contentRect.height) / 2;
          left = triggerRect.left - contentRect.width - gap;
          break;
        case 'right':
          top = triggerRect.top + (triggerRect.height - contentRect.height) / 2;
          left = triggerRect.right + gap;
          break;
      }
      
      // Constrain to viewport
      top = Math.max(padding, Math.min(top, viewportHeight - contentRect.height - padding));
      left = Math.max(padding, Math.min(left, viewportWidth - contentRect.width - padding));
      
      // Apply position
      this.content.style.top = `${top}px`;
      this.content.style.left = `${left}px`;
      
      // Handle alignment adjustments for horizontal positions
      if ((actualSide === 'top' || actualSide === 'bottom') && this.align === 'center') {
        const contentHalfWidth = contentRect.width / 2;
        const triggerCenter = triggerRect.left + triggerRect.width / 2;
        
        // Check if centered tooltip would overflow viewport
        if (triggerCenter - contentHalfWidth < padding) {
          this.content.setAttribute('data-align', 'start');
        } else if (triggerCenter + contentHalfWidth > viewportWidth - padding) {
          this.content.setAttribute('data-align', 'end');
        }
      }
    }
    
    clearTimeouts() {
      if (this.openTimeout) {
        clearTimeout(this.openTimeout);
        this.openTimeout = null;
      }
      if (this.closeTimeout) {
        clearTimeout(this.closeTimeout);
        this.closeTimeout = null;
      }
    }
    
    generateId() {
      return `pm7-tooltip-${Math.random().toString(36).substr(2, 9)}`;
    }
    
    announceToScreenReaders() {
      // Create a live region for screen reader announcements
      const announcement = document.createElement('div');
      announcement.setAttribute('role', 'status');
      announcement.setAttribute('aria-live', 'polite');
      announcement.className = 'pm7-sr-only';
      announcement.textContent = this.content.textContent;
      
      document.body.appendChild(announcement);
      
      // Remove after announcement
      setTimeout(() => {
        document.body.removeChild(announcement);
      }, 1000);
    }
    
    destroy() {
      this.cleanup();
      PM7Tooltip.instances.delete(this.element);
      delete this.element._pm7TooltipInstance;
      
      // Remove global event listeners
      document.removeEventListener('click', this.handleDocumentClick);
      document.removeEventListener('keydown', this.handleKeyDown);
      window.removeEventListener('scroll', this.updatePosition, true);
      window.removeEventListener('resize', this.updatePosition);
      
      // Reset state
      this.hide();
    }
  }

  // Self-healing function
  function healTooltips$1() {
    // Find all tooltips that were initialized but lost their instance
    const lostTooltips = document.querySelectorAll('[data-pm7-tooltip][data-pm7-tooltip-initialized]:not([data-pm7-tooltip-healing])');
    
    lostTooltips.forEach(tooltip => {
      if (!tooltip._pm7TooltipInstance || !PM7Tooltip.instances.has(tooltip)) {
        tooltip.setAttribute('data-pm7-tooltip-healing', 'true');
        console.log('[PM7Tooltip] Healing tooltip:', tooltip);
        new PM7Tooltip(tooltip);
        tooltip.removeAttribute('data-pm7-tooltip-healing');
      }
    });
  }

  // Auto-initialize tooltips
  function initTooltips(container = document) {
    // Find all elements with data-pm7-tooltip attribute
    const tooltips = container.querySelectorAll('[data-pm7-tooltip]:not([data-pm7-tooltip-initialized])');
    tooltips.forEach(tooltip => {
      // Skip if this element is already part of a tooltip structure (e.g., the wrapper div)
      if (!tooltip.classList.contains('pm7-tooltip-trigger') && !tooltip.classList.contains('pm7-tooltip-content')) {
        new PM7Tooltip(tooltip);
      }
    });
  }

  // Make healing function available
  PM7Tooltip.heal = healTooltips$1;

  // Initialize on DOM ready
  if (typeof document !== 'undefined') {
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', () => initTooltips());
    } else {
      initTooltips();
    }
  }

  /**
   * PM7 Accordion Component with Self-Healing
   * Collapsible content panels with automatic recovery from framework re-renders
   */
  class PM7Accordion {
    static instances = new WeakMap(); // Use WeakMap for better memory management
    
    constructor(element, options = {}) {
      // Self-healing: Check if element was re-rendered by framework
      const wasInitialized = element.hasAttribute('data-pm7-accordion-initialized');
      const hasInstance = PM7Accordion.instances.has(element);
      
      // If initialized but no instance, element was re-rendered
      if (wasInitialized && !hasInstance) {
        console.log('[PM7Accordion] Self-healing: Re-initializing accordion after framework re-render');
        element.removeAttribute('data-pm7-accordion-initialized');
      }
      
      // Check if this element already has an accordion instance
      if (PM7Accordion.instances.has(element)) {
        return PM7Accordion.instances.get(element);
      }
      
      this.element = element;
      
      // Preserve state if this is a re-render
      const preservedState = this.preserveState();
      
      // AI-Agent FIRST: Automatically add pm7-accordion class if missing
      if (!this.element.classList.contains('pm7-accordion')) {
        this.element.classList.add('pm7-accordion');
      }
      
      this.options = {
        allowMultiple: false,
        defaultOpen: null,
        iconPosition: 'end', // 'start' or 'end'
        textAlign: 'left', // 'left', 'center', or 'right'
        height: null, // 'sm', 'md', 'lg', 'fixed' or null for auto
        fixedHeight: 300, // height in pixels when using 'fixed' option
        ...options
      };
      
      this.items = [];
      this.eventListeners = new Map(); // Track event listeners for cleanup
      
      // Store this instance
      PM7Accordion.instances.set(element, this);
      
      // Store instance reference on element for self-healing
      element._pm7AccordionInstance = this;
      
      this.init();
      
      // Restore state if this was a re-render
      if (preservedState && preservedState.openItems.length > 0) {
        this.restoreState(preservedState);
      }
      
      // Mark as initialized
      element.setAttribute('data-pm7-accordion-initialized', 'true');
    }
    
    preserveState() {
      // Try to preserve state from DOM
      const items = this.element.querySelectorAll('.pm7-accordion-item');
      const openItems = [];
      
      items.forEach((item, index) => {
        if (item.getAttribute('data-state') === 'open') {
          openItems.push(index);
        }
      });
      
      return {
        openItems,
        options: this.element.dataset // Preserve any data attributes
      };
    }
    
    restoreState(state) {
      // Restore open items
      state.openItems.forEach(index => {
        if (index < this.items.length) {
          // Use setTimeout to ensure DOM is ready
          setTimeout(() => {
            this.open(index);
          }, 0);
        }
      });
    }

    init() {
      // Remove any existing event listeners to prevent duplicates
      this.cleanup();
      
      // Apply icon position class if needed
      if (this.options.iconPosition === 'start') {
        this.element.classList.add('pm7-accordion--icon-start');
      }
      
      // Apply text alignment class if needed
      if (this.options.textAlign && this.options.textAlign !== 'left') {
        this.element.classList.add(`pm7-accordion--text-${this.options.textAlign}`);
      }
      
      // Apply height class if needed
      if (this.options.height) {
        this.element.classList.add(`pm7-accordion--height-${this.options.height}`);
        
        // If fixed height, set the CSS variable
        if (this.options.height === 'fixed' && this.options.fixedHeight) {
          this.element.style.setProperty('--pm7-accordion-fixed-height', `${this.options.fixedHeight}px`);
        }
      }
      
      // Find all accordion items
      const items = this.element.querySelectorAll('.pm7-accordion-item');
      
      items.forEach((item, index) => {
        const trigger = item.querySelector('.pm7-accordion-trigger');
        const content = item.querySelector('.pm7-accordion-content');
        
        if (!trigger || !content) return;
        
        // Add chevron icon if not present
        let icon = trigger.querySelector('.pm7-accordion-icon');
        if (!icon) {
          icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
          icon.setAttribute('class', 'pm7-accordion-icon');
          icon.setAttribute('width', '16');
          icon.setAttribute('height', '16');
          icon.setAttribute('viewBox', '0 0 12 12');
          icon.innerHTML = '<path d="M2.5 4L6 7.5L9.5 4" stroke="currentColor" stroke-width="1.5" fill="none"/>';
          trigger.appendChild(icon);
        }
        
        // Store item reference
        this.items.push({ item, trigger, content });
        
        // Set initial state
        const isOpen = this.options.defaultOpen === index || 
                       this.options.defaultOpen === 'all' ||
                       item.getAttribute('data-state') === 'open';
        
        // Mark item as initial to prevent animation on already open items
        if (isOpen) {
          item.setAttribute('data-initial', 'true');
        }
        
        this.setItemState(item, trigger, content, isOpen);
        
        // Remove initial flag after initialization
        if (isOpen) {
          setTimeout(() => {
            item.removeAttribute('data-initial');
          }, 50);
        }
        
        // Create bound handlers for proper cleanup
        const clickHandler = () => this.toggle(index);
        const keyHandler = (e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            this.toggle(index);
          }
        };
        
        // Add event listeners
        trigger.addEventListener('click', clickHandler);
        trigger.addEventListener('keydown', keyHandler);
        
        // Store listeners for cleanup
        this.eventListeners.set(`click-${index}`, { element: trigger, type: 'click', handler: clickHandler });
        this.eventListeners.set(`keydown-${index}`, { element: trigger, type: 'keydown', handler: keyHandler });
      });
    }
    
    cleanup() {
      // Remove all event listeners
      this.eventListeners.forEach(({ element, type, handler }) => {
        element.removeEventListener(type, handler);
      });
      this.eventListeners.clear();
    }
    
    destroy() {
      this.cleanup();
      PM7Accordion.instances.delete(this.element);
      delete this.element._pm7AccordionInstance;
    }
    
    toggle(index) {
      const { item, trigger, content } = this.items[index];
      const isOpen = item.getAttribute('data-state') === 'open';
      
      // If closing, just close this item
      if (isOpen) {
        this.setItemState(item, trigger, content, false);
      } else {
        // If opening and allowMultiple is false, close other items
        if (!this.options.allowMultiple) {
          this.items.forEach((otherItem, otherIndex) => {
            if (otherIndex !== index && otherItem.item.getAttribute('data-state') === 'open') {
              this.setItemState(otherItem.item, otherItem.trigger, otherItem.content, false);
            }
          });
        }
        
        // Open this item
        this.setItemState(item, trigger, content, true);
      }
      
      // Dispatch custom event
      this.element.dispatchEvent(new CustomEvent('pm7:accordion:toggle', {
        detail: { index, isOpen: !isOpen },
        bubbles: true
      }));
    }
    
    setItemState(item, trigger, content, isOpen) {
      if (isOpen) {
        // Opening
        item.setAttribute('data-state', 'open');
        trigger.setAttribute('aria-expanded', 'true');
        content.setAttribute('data-state', 'open');
        
        // Set height for animation
        const height = content.scrollHeight;
        content.style.setProperty('--pm7-accordion-content-height', `${height}px`);
      } else {
        // Closing - only animate if it was previously open
        if (content.getAttribute('data-state') === 'open') {
          // Set height before closing
          const height = content.scrollHeight;
          content.style.setProperty('--pm7-accordion-content-height', `${height}px`);
          
          // Set closing state for animation
          content.setAttribute('data-state', 'closing');
          
          // After animation completes, set to closed
          setTimeout(() => {
            item.setAttribute('data-state', 'closed');
            trigger.setAttribute('aria-expanded', 'false');
            content.setAttribute('data-state', 'closed');
          }, 250); // Match animation duration
        } else {
          // Initial closed state - no animation
          item.setAttribute('data-state', 'closed');
          trigger.setAttribute('aria-expanded', 'false');
          content.setAttribute('data-state', 'closed');
        }
      }
    }
    
    // Public methods
    open(index) {
      if (index >= 0 && index < this.items.length) {
        const { item, trigger, content } = this.items[index];
        this.setItemState(item, trigger, content, true);
      }
    }
    
    close(index) {
      if (index >= 0 && index < this.items.length) {
        const { item, trigger, content } = this.items[index];
        this.setItemState(item, trigger, content, false);
      }
    }
    
    openAll() {
      this.items.forEach(({ item, trigger, content }) => {
        this.setItemState(item, trigger, content, true);
      });
    }
    
    closeAll() {
      this.items.forEach(({ item, trigger, content }) => {
        this.setItemState(item, trigger, content, false);
      });
    }
    
    // Static auto-init method
    static autoInit() {
      const accordions = document.querySelectorAll('[data-pm7-accordion]');
      accordions.forEach(accordion => {
        // Skip if already initialized and has instance
        if (accordion.hasAttribute('data-pm7-accordion-initialized') && accordion._pm7AccordionInstance) {
          return;
        }
        
        // Initialize accordion
        const allowMultiple = accordion.getAttribute('data-allow-multiple') === 'true';
        const defaultOpen = accordion.getAttribute('data-default-open');
        const iconPosition = accordion.getAttribute('data-icon-position') || 'end';
        const textAlign = accordion.getAttribute('data-text-align') || 'left';
        const height = accordion.getAttribute('data-height');
        const fixedHeight = accordion.getAttribute('data-fixed-height');
        
        new PM7Accordion(accordion, {
          allowMultiple,
          defaultOpen: defaultOpen === 'all' ? 'all' : parseInt(defaultOpen),
          iconPosition,
          textAlign,
          height,
          fixedHeight: fixedHeight ? parseInt(fixedHeight) : 300
        });
      });
    }
  }

  // Self-healing function
  function healAccordions$1() {
    // Find all accordions that were initialized but lost their instance
    const lostAccordions = document.querySelectorAll('[data-pm7-accordion][data-pm7-accordion-initialized]:not([data-pm7-accordion-healing])');
    
    lostAccordions.forEach(accordion => {
      if (!accordion._pm7AccordionInstance || !PM7Accordion.instances.has(accordion)) {
        accordion.setAttribute('data-pm7-accordion-healing', 'true');
        console.log('[PM7Accordion] Healing accordion:', accordion);
        PM7Accordion.autoInit(); // Re-init just this accordion
        accordion.removeAttribute('data-pm7-accordion-healing');
      }
    });
  }

  // Auto-initialize accordions on DOM ready
  if (typeof document !== 'undefined') {
    // Initialize immediately if DOM is ready
    if (document.readyState === 'loading') {
      document.addEventListener('DOMContentLoaded', PM7Accordion.autoInit, { once: true });
    } else {
      setTimeout(PM7Accordion.autoInit, 0);
    }
  }

  // Make healing function available
  PM7Accordion.heal = healAccordions$1;

  /**
   * PM7 Theme Switch Component
   * A toggle switch for switching between light and dark themes
   */
  class PM7ThemeSwitch {
    constructor(element, options = {}) {
      this.element = element;
      this.options = {
        defaultTheme: null, // 'light', 'dark', or null for auto-detect
        onChange: null, // Callback function
        storageKey: 'pm7-theme',
        applyToRoot: true, // Whether to apply theme class to document root
        ...options
      };
      
      this.button = null;
      this.currentTheme = null;
      this.init();
    }

    init() {
      // Add base class if not present
      if (!this.element.classList.contains('pm7-theme-switch')) {
        this.element.classList.add('pm7-theme-switch');
      }
      
      // Get or create button
      this.button = this.element.querySelector('.pm7-theme-switch-button');
      if (!this.button) {
        this.createButton();
      }
      
      // Initialize theme
      this.currentTheme = this.getInitialTheme();
      this.updateTheme(this.currentTheme, false);
      
      // Add event listeners
      this.button.addEventListener('click', () => this.toggle());
      this.button.addEventListener('keydown', (e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          this.toggle();
        }
      });
      
      // Listen for system theme changes
      if (window.matchMedia) {
        const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
        mediaQuery.addEventListener('change', (e) => {
          if (!localStorage.getItem(this.options.storageKey)) {
            this.updateTheme(e.matches ? 'dark' : 'light');
          }
        });
      }
    }
    
    createButton() {
      // Create button structure
      this.button = document.createElement('button');
      this.button.className = 'pm7-theme-switch-button';
      this.button.setAttribute('type', 'button');
      this.button.setAttribute('role', 'switch');
      this.button.setAttribute('aria-label', 'Toggle theme');
      
      // Create thumb with icons
      const thumb = document.createElement('div');
      thumb.className = 'pm7-theme-switch-thumb';
      
      // Sun icon (light mode)
      const sunIcon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      sunIcon.setAttribute('class', 'pm7-theme-switch-icon pm7-theme-switch-sun');
      sunIcon.setAttribute('viewBox', '0 0 24 24');
      sunIcon.setAttribute('fill', 'none');
      sunIcon.setAttribute('stroke', 'currentColor');
      sunIcon.setAttribute('stroke-width', '2');
      sunIcon.setAttribute('stroke-linecap', 'round');
      sunIcon.setAttribute('stroke-linejoin', 'round');
      sunIcon.innerHTML = `
      <circle cx="12" cy="12" r="5"></circle>
      <line x1="12" y1="1" x2="12" y2="3"></line>
      <line x1="12" y1="21" x2="12" y2="23"></line>
      <line x1="4.22" y1="4.22" x2="5.64" y2="5.64"></line>
      <line x1="18.36" y1="18.36" x2="19.78" y2="19.78"></line>
      <line x1="1" y1="12" x2="3" y2="12"></line>
      <line x1="21" y1="12" x2="23" y2="12"></line>
      <line x1="4.22" y1="19.78" x2="5.64" y2="18.36"></line>
      <line x1="18.36" y1="5.64" x2="19.78" y2="4.22"></line>
    `;
      
      // Moon icon (dark mode)
      const moonIcon = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
      moonIcon.setAttribute('class', 'pm7-theme-switch-icon pm7-theme-switch-moon');
      moonIcon.setAttribute('viewBox', '0 0 24 24');
      moonIcon.setAttribute('fill', 'none');
      moonIcon.setAttribute('stroke', 'currentColor');
      moonIcon.setAttribute('stroke-width', '2');
      moonIcon.setAttribute('stroke-linecap', 'round');
      moonIcon.setAttribute('stroke-linejoin', 'round');
      moonIcon.innerHTML = '<path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"></path>';
      
      thumb.appendChild(sunIcon);
      thumb.appendChild(moonIcon);
      this.button.appendChild(thumb);
      this.element.appendChild(this.button);
    }
    
    getInitialTheme() {
      // Check for explicit default theme
      if (this.options.defaultTheme) {
        return this.options.defaultTheme;
      }
      
      // Check localStorage
      const savedTheme = localStorage.getItem(this.options.storageKey);
      if (savedTheme === 'light' || savedTheme === 'dark') {
        return savedTheme;
      }
      
      // Check system preference
      if (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) {
        return 'dark';
      }
      
      return 'light';
    }
    
    toggle() {
      const newTheme = this.currentTheme === 'light' ? 'dark' : 'light';
      this.updateTheme(newTheme);
    }
    
    updateTheme(theme, persist = true) {
      this.currentTheme = theme;
      
      // Update component state
      this.element.setAttribute('data-theme', theme);
      this.button.setAttribute('aria-checked', theme === 'dark' ? 'true' : 'false');
      this.button.setAttribute('aria-label', `Switch to ${theme === 'light' ? 'dark' : 'light'} mode`);
      
      // Apply theme to document root if enabled
      if (this.options.applyToRoot) {
        document.documentElement.classList.toggle('dark', theme === 'dark');
        document.body.classList.toggle('dark', theme === 'dark');
      }
      
      // Persist to localStorage
      if (persist) {
        localStorage.setItem(this.options.storageKey, theme);
      }
      
      // Call onChange callback
      if (this.options.onChange && persist) {
        this.options.onChange(theme);
      }
    }
    
    // Public methods
    setTheme(theme) {
      if (theme === 'light' || theme === 'dark') {
        this.updateTheme(theme);
      }
    }
    
    getTheme() {
      return this.currentTheme;
    }
    
    // Static auto-init method
    static autoInit() {
      const switches = document.querySelectorAll('[data-pm7-theme-switch]');
      switches.forEach(switchElement => {
        // Initialize theme switch
        const defaultTheme = switchElement.getAttribute('data-default-theme');
        const storageKey = switchElement.getAttribute('data-storage-key') || 'pm7-theme';
        const applyToRoot = switchElement.getAttribute('data-apply-to-root') !== 'false';
        
        new PM7ThemeSwitch(switchElement, {
          defaultTheme,
          storageKey,
          applyToRoot
        });
      });
    }
  }

  // Auto-initialize on DOM ready
  if (typeof document !== 'undefined') {
    document.addEventListener('DOMContentLoaded', () => {
      PM7ThemeSwitch.autoInit();
    });
  }

  /**
   * PM7 Sidebar Component
   * Handles sidebar interactions, animations, and state management
   */

  class PM7Sidebar {
    static instances = new WeakMap();
    
    constructor(element) {
      // Self-healing: Check if element was re-rendered by framework
      const wasInitialized = element.hasAttribute('data-pm7-initialized');
      const hasInstance = PM7Sidebar.instances.has(element);
      
      // If initialized but no instance, element was re-rendered
      if (wasInitialized && !hasInstance) {
        console.log('[PM7Sidebar] Self-healing: Re-initializing sidebar after framework re-render');
        element.removeAttribute('data-pm7-initialized');
      }
      
      // Check if this element already has a sidebar instance
      if (PM7Sidebar.instances.has(element)) {
        return PM7Sidebar.instances.get(element);
      }
      
      this.element = element;
      this.isOpen = false;
      this.overlay = null;
      this.triggers = [];
      this.closeButton = null;
      this.pushElement = null;
      this.position = 'left';
      this.mode = 'overlay'; // overlay, push, or static
      this.eventListeners = new Map();
      
      // Preserve state if this is a re-render
      const preservedState = this.preserveState();
      
      // Store this instance
      PM7Sidebar.instances.set(element, this);
      
      // Store instance reference on element for self-healing
      element._pm7SidebarInstance = this;
      
      this.init();
      
      // Restore state if this was a re-render
      if (preservedState && preservedState.wasOpen) {
        this.restoreState(preservedState);
      }
      
      // Mark as initialized
      element.setAttribute('data-pm7-initialized', 'true');
    }
    
    preserveState() {
      // Check current state
      const wasOpen = this.element.classList.contains('pm7-sidebar--open') || 
                      this.element.dataset.state === 'open';
      
      // Check collapsible states
      const collapsibles = this.element.querySelectorAll('.pm7-sidebar-collapsible');
      const collapsibleStates = [];
      collapsibles.forEach((collapsible, index) => {
        collapsibleStates.push({
          index,
          isOpen: collapsible.dataset.state === 'open'
        });
      });
      
      return {
        wasOpen,
        collapsibleStates,
        position: this.element.classList.contains('pm7-sidebar--right') ? 'right' : 'left'
      };
    }
    
    restoreState(state) {
      // Restore open state
      if (state.wasOpen && this.mode !== 'static') {
        setTimeout(() => {
          this.open();
        }, 50);
      }
      
      // Restore collapsible states
      const collapsibles = this.element.querySelectorAll('.pm7-sidebar-collapsible');
      state.collapsibleStates.forEach(({ index, isOpen }) => {
        const collapsible = collapsibles[index];
        if (collapsible) {
          collapsible.dataset.state = isOpen ? 'open' : 'closed';
          const trigger = collapsible.querySelector('.pm7-sidebar-collapsible-trigger');
          const content = collapsible.querySelector('.pm7-sidebar-collapsible-content');
          if (trigger) trigger.setAttribute('aria-expanded', isOpen);
          if (content) content.setAttribute('aria-hidden', !isOpen);
        }
      });
    }
    
    cleanup() {
      // Remove all tracked event listeners
      this.eventListeners.forEach(({ element, type, handler }) => {
        element.removeEventListener(type, handler);
      });
      this.eventListeners.clear();
    }

    init() {
      // Set initial state
      this.isOpen = this.element.classList.contains('pm7-sidebar--open') || 
                    this.element.dataset.state === 'open';
      
      // Determine position
      this.position = this.element.classList.contains('pm7-sidebar--right') ? 'right' : 'left';
      
      // Determine mode
      if (this.element.classList.contains('pm7-sidebar--static')) {
        this.mode = 'static';
      } else if (document.querySelector('.pm7-sidebar-push')) {
        this.mode = 'push';
        this.pushElement = document.querySelector('.pm7-sidebar-push');
      }
      
      // Create overlay if needed
      if (this.mode === 'overlay') {
        this.createOverlay();
      }
      
      // Find and setup triggers
      this.setupTriggers();
      
      // Setup close button
      this.setupCloseButton();
      
      // Setup keyboard navigation
      this.setupKeyboardNavigation();
      
      // Setup collapsible sections
      this.setupCollapsibles();
      
      // Handle escape key
      this.handleEscapeKey();
      
      // Set ARIA attributes
      this.updateAriaAttributes();
    }

    createOverlay() {
      this.overlay = document.createElement('div');
      this.overlay.className = 'pm7-sidebar-overlay';
      this.overlay.setAttribute('aria-hidden', 'true');
      
      // Insert overlay after sidebar
      this.element.parentNode.insertBefore(this.overlay, this.element.nextSibling);
      
      // Click overlay to close
      this.overlay.addEventListener('click', () => this.close());
    }

    setupTriggers() {
      // Find all triggers for this sidebar
      const sidebarId = this.element.id;
      if (sidebarId) {
        this.triggers = document.querySelectorAll(`[data-pm7-sidebar-trigger="${sidebarId}"]`);
      }
      
      // Also check for generic triggers if sidebar has data-pm7-sidebar attribute
      if (this.element.hasAttribute('data-pm7-sidebar')) {
        const genericTriggers = document.querySelectorAll('[data-pm7-sidebar-trigger]');
        genericTriggers.forEach(trigger => {
          if (!trigger.dataset.pm7SidebarTrigger) {
            this.triggers = [...this.triggers, trigger];
          }
        });
      }
      
      // Add click listeners to triggers
      this.triggers.forEach((trigger, index) => {
        const clickHandler = (e) => {
          e.preventDefault();
          this.toggle();
        };
        
        trigger.addEventListener('click', clickHandler);
        this.eventListeners.set(`trigger-click-${index}`, { element: trigger, type: 'click', handler: clickHandler });
        
        // Set ARIA attributes
        trigger.setAttribute('aria-controls', sidebarId || '');
        trigger.setAttribute('aria-expanded', this.isOpen);
      });
    }

    setupCloseButton() {
      this.closeButton = this.element.querySelector('.pm7-sidebar-close');
      if (this.closeButton) {
        const closeHandler = () => this.close();
        this.closeButton.addEventListener('click', closeHandler);
        this.eventListeners.set('close-click', { element: this.closeButton, type: 'click', handler: closeHandler });
        this.closeButton.setAttribute('aria-label', 'Close sidebar');
      }
    }

    setupKeyboardNavigation() {
      const navItems = this.element.querySelectorAll('.pm7-sidebar-item');
      
      navItems.forEach((item, index) => {
        item.setAttribute('tabindex', '0');
        
        item.addEventListener('keydown', (e) => {
          switch (e.key) {
            case 'ArrowDown':
              e.preventDefault();
              const nextItem = navItems[index + 1] || navItems[0];
              nextItem.focus();
              break;
              
            case 'ArrowUp':
              e.preventDefault();
              const prevItem = navItems[index - 1] || navItems[navItems.length - 1];
              prevItem.focus();
              break;
              
            case 'Home':
              e.preventDefault();
              navItems[0].focus();
              break;
              
            case 'End':
              e.preventDefault();
              navItems[navItems.length - 1].focus();
              break;
          }
        });
      });
    }

    setupCollapsibles() {
      const collapsibles = this.element.querySelectorAll('.pm7-sidebar-collapsible');
      
      collapsibles.forEach(collapsible => {
        const trigger = collapsible.querySelector('.pm7-sidebar-collapsible-trigger');
        const content = collapsible.querySelector('.pm7-sidebar-collapsible-content');
        
        if (trigger && content) {
          // Set initial state
          const isOpen = collapsible.dataset.state === 'open';
          trigger.setAttribute('aria-expanded', isOpen);
          content.setAttribute('aria-hidden', !isOpen);
          
          // Toggle on click
          trigger.addEventListener('click', () => {
            const currentlyOpen = collapsible.dataset.state === 'open';
            collapsible.dataset.state = currentlyOpen ? 'closed' : 'open';
            trigger.setAttribute('aria-expanded', !currentlyOpen);
            content.setAttribute('aria-hidden', currentlyOpen);
            
            // Dispatch custom event
            this.element.dispatchEvent(new CustomEvent('pm7:sidebar:collapsible:toggle', {
              detail: { 
                collapsible, 
                isOpen: !currentlyOpen 
              },
              bubbles: true
            }));
          });
        }
      });
    }

    handleEscapeKey() {
      const escapeHandler = (e) => {
        if (e.key === 'Escape' && this.isOpen && this.mode !== 'static') {
          this.close();
        }
      };
      
      document.addEventListener('keydown', escapeHandler);
      this.eventListeners.set('escape-key', { element: document, type: 'keydown', handler: escapeHandler });
    }

    open() {
      if (this.isOpen || this.mode === 'static') return;
      
      this.isOpen = true;
      this.element.classList.add('pm7-sidebar--open');
      this.element.dataset.state = 'open';
      
      // Show overlay
      if (this.overlay) {
        this.overlay.classList.add('pm7-sidebar-overlay--visible');
      }
      
      // Push content
      if (this.pushElement) {
        this.pushElement.classList.add('pm7-sidebar-push--active');
        if (this.position === 'right') {
          this.pushElement.classList.add('pm7-sidebar-push--right');
        }
      }
      
      // Update ARIA
      this.updateAriaAttributes();
      
      // Focus management
      this.element.setAttribute('tabindex', '-1');
      this.element.focus();
      
      // Trap focus
      this.trapFocus();
      
      // Dispatch event
      this.element.dispatchEvent(new CustomEvent('pm7:sidebar:open', {
        detail: { sidebar: this },
        bubbles: true
      }));
    }

    close() {
      if (!this.isOpen || this.mode === 'static') return;
      
      this.isOpen = false;
      this.element.classList.remove('pm7-sidebar--open');
      this.element.dataset.state = 'closed';
      
      // Hide overlay
      if (this.overlay) {
        this.overlay.classList.remove('pm7-sidebar-overlay--visible');
      }
      
      // Reset push
      if (this.pushElement) {
        this.pushElement.classList.remove('pm7-sidebar-push--active');
      }
      
      // Update ARIA
      this.updateAriaAttributes();
      
      // Release focus trap
      this.releaseFocusTrap();
      
      // Return focus to trigger
      const activeTrigger = document.activeElement;
      if (this.triggers.length && !this.triggers.includes(activeTrigger)) {
        this.triggers[0].focus();
      }
      
      // Dispatch event
      this.element.dispatchEvent(new CustomEvent('pm7:sidebar:close', {
        detail: { sidebar: this },
        bubbles: true
      }));
    }

    toggle() {
      if (this.isOpen) {
        this.close();
      } else {
        this.open();
      }
    }

    updateAriaAttributes() {
      this.element.setAttribute('aria-hidden', !this.isOpen);
      
      this.triggers.forEach(trigger => {
        trigger.setAttribute('aria-expanded', this.isOpen);
      });
      
      if (this.overlay) {
        this.overlay.setAttribute('aria-hidden', !this.isOpen);
      }
    }

    trapFocus() {
      const focusableElements = this.element.querySelectorAll(
        'a[href], button, textarea, input[type="text"], input[type="radio"], input[type="checkbox"], select, [tabindex]:not([tabindex="-1"])'
      );
      
      const firstFocusable = focusableElements[0];
      const lastFocusable = focusableElements[focusableElements.length - 1];
      
      this.focusTrapHandler = (e) => {
        if (e.key !== 'Tab') return;
        
        if (e.shiftKey) {
          if (document.activeElement === firstFocusable) {
            e.preventDefault();
            lastFocusable.focus();
          }
        } else {
          if (document.activeElement === lastFocusable) {
            e.preventDefault();
            firstFocusable.focus();
          }
        }
      };
      
      this.element.addEventListener('keydown', this.focusTrapHandler);
    }

    releaseFocusTrap() {
      if (this.focusTrapHandler) {
        this.element.removeEventListener('keydown', this.focusTrapHandler);
        this.focusTrapHandler = null;
      }
    }

    destroy() {
      // Remove all event listeners
      this.cleanup();
      
      if (this.overlay) {
        this.overlay.remove();
      }
      
      this.releaseFocusTrap();
      
      // Reset element
      this.element.classList.remove('pm7-sidebar--open');
      this.element.removeAttribute('data-pm7-initialized');
      
      // Remove from instances
      PM7Sidebar.instances.delete(this.element);
      delete this.element._pm7SidebarInstance;
      delete this.element.PM7Sidebar;
    }
  }

  // Self-healing function
  function healSidebars$1() {
    // Find all sidebars that were initialized but lost their instance
    const lostSidebars = document.querySelectorAll('[data-pm7-sidebar][data-pm7-initialized]:not([data-pm7-sidebar-healing])');
    
    lostSidebars.forEach(sidebar => {
      if (!sidebar._pm7SidebarInstance || !PM7Sidebar.instances.has(sidebar)) {
        sidebar.setAttribute('data-pm7-sidebar-healing', 'true');
        console.log('[PM7Sidebar] Healing sidebar:', sidebar);
        new PM7Sidebar(sidebar);
        sidebar.removeAttribute('data-pm7-sidebar-healing');
      }
    });
  }

  // Auto-initialization
  function initSidebars() {
    const sidebars = document.querySelectorAll('[data-pm7-sidebar]:not([data-pm7-initialized])');
    
    sidebars.forEach(sidebar => {
      new PM7Sidebar(sidebar);
    });
    
    // Also initialize any standalone triggers
    const triggers = document.querySelectorAll('[data-pm7-sidebar-trigger]');
    triggers.forEach(trigger => {
      const targetId = trigger.dataset.pm7SidebarTrigger;
      if (targetId) {
        const sidebar = document.getElementById(targetId);
        if (sidebar && !sidebar._pm7SidebarInstance && !sidebar.hasAttribute('data-pm7-initialized')) {
          new PM7Sidebar(sidebar);
        }
      }
    });
  }

  // Make healing function available
  PM7Sidebar.heal = healSidebars$1;

  // Initialize on DOM ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initSidebars);
  } else {
    initSidebars();
  }

  /**
   * PM7 Hamburger Menu Icon
   * 
   * Creates the standard PM7 hamburger menu icon
   * @param {Object} options - Configuration options
   * @param {number} options.width - Icon width (default: 18)
   * @param {number} options.height - Icon height (default: 15)
   * @param {string} options.color - Icon color (default: currentColor)
   * @param {string} options.className - CSS class name
   * @returns {string} SVG string
   */
  function createHamburgerIcon(options = {}) {
    const {
      width = 18,
      height = 15,
      color = 'currentColor',
      className = ''
    } = options;

    return `<svg width="${width}" height="${height}" viewBox="0 0 18 15" fill="none" xmlns="http://www.w3.org/2000/svg" class="${className}">
  <rect width="18" height="2.5" rx="1.25" fill="${color}"/>
  <rect y="6.25" width="18" height="2.5" rx="1.25" fill="${color}"/>
  <rect y="12.5" width="18" height="2.5" rx="1.25" fill="${color}"/>
</svg>`;
  }

  /**
   * PM7 Hamburger Menu Icon as DOM element
   * 
   * Creates the standard PM7 hamburger menu icon as a DOM element
   * @param {Object} options - Configuration options
   * @param {number} options.width - Icon width (default: 18)
   * @param {number} options.height - Icon height (default: 15)
   * @param {string} options.color - Icon color (default: currentColor)
   * @param {string} options.className - CSS class name
   * @returns {SVGElement} SVG DOM element
   */
  function createHamburgerIconElement(options = {}) {
    const {
      width = 18,
      height = 15,
      color = 'currentColor',
      className = ''
    } = options;

    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    svg.setAttribute('width', width);
    svg.setAttribute('height', height);
    svg.setAttribute('viewBox', '0 0 18 15');
    svg.setAttribute('fill', 'none');
    
    if (className) {
      svg.setAttribute('class', className);
    }

    // Create the three bars
    const bars = [
      { y: 0 },
      { y: 6.25 },
      { y: 12.5 }
    ];

    bars.forEach(({ y }) => {
      const rect = document.createElementNS('http://www.w3.org/2000/svg', 'rect');
      rect.setAttribute('width', '18');
      rect.setAttribute('height', '2.5');
      rect.setAttribute('rx', '1.25');
      rect.setAttribute('y', y.toString());
      rect.setAttribute('fill', color);
      svg.appendChild(rect);
    });

    return svg;
  }

  /**
   * Get the PM7 hamburger icon as data URI
   * Useful for CSS background-image
   * @param {string} color - Icon color (default: black)
   * @returns {string} Data URI
   */
  function getHamburgerIconDataURI(color = '%23000000') {
    // Note: color should be URL encoded (# becomes %23)
    return `data:image/svg+xml,%3Csvg width='18' height='15' viewBox='0 0 18 15' fill='none' xmlns='http://www.w3.org/2000/svg'%3E%3Crect width='18' height='2.5' rx='1.25' fill='${color}'/%3E%3Crect y='6.25' width='18' height='2.5' rx='1.25' fill='${color}'/%3E%3Crect y='12.5' width='18' height='2.5' rx='1.25' fill='${color}'/%3E%3C/svg%3E`;
  }

  /**
   * PM7 Core JavaScript Components
   * Export all interactive components
   */


  // Self-healing function for menus
  function healMenus() {
    // Find all menus that were initialized but lost their instance
    const lostMenus = document.querySelectorAll('[data-pm7-menu][data-pm7-menu-initialized]:not([data-pm7-menu-healing])');
    
    lostMenus.forEach(menu => {
      if (!menu._pm7MenuInstance || !PM7Menu.instances.has(menu)) {
        menu.setAttribute('data-pm7-menu-healing', 'true');
        console.log('[PM7Menu] Healing menu:', menu);
        new PM7Menu(menu);
        menu.removeAttribute('data-pm7-menu-healing');
      }
    });
  }

  // Self-healing function for accordions
  function healAccordions() {
    // Find all accordions that were initialized but lost their instance
    const lostAccordions = document.querySelectorAll('[data-pm7-accordion][data-pm7-accordion-initialized]:not([data-pm7-accordion-healing])');
    
    lostAccordions.forEach(accordion => {
      if (!accordion._pm7AccordionInstance || !PM7Accordion.instances.has(accordion)) {
        accordion.setAttribute('data-pm7-accordion-healing', 'true');
        console.log('[PM7Accordion] Healing accordion:', accordion);
        PM7Accordion.autoInit(); // Re-init just this accordion
        accordion.removeAttribute('data-pm7-accordion-healing');
      }
    });
  }

  // Self-healing function for tab selectors
  function healTabSelectors() {
    // Find all tab selectors that were initialized but lost their instance
    const lostTabSelectors = document.querySelectorAll('[data-pm7-tab-selector][data-pm7-tab-initialized]:not([data-pm7-tab-healing])');
    
    lostTabSelectors.forEach(selector => {
      if (!selector._pm7TabSelectorInstance || !PM7TabSelector.instances.has(selector)) {
        selector.setAttribute('data-pm7-tab-healing', 'true');
        console.log('[PM7TabSelector] Healing tab selector:', selector);
        new PM7TabSelector(selector);
        selector.removeAttribute('data-pm7-tab-healing');
      }
    });
  }

  // Self-healing function for tooltips
  function healTooltips() {
    // Find all tooltips that were initialized but lost their instance
    const lostTooltips = document.querySelectorAll('[data-pm7-tooltip][data-pm7-tooltip-initialized]:not([data-pm7-tooltip-healing])');
    
    lostTooltips.forEach(tooltip => {
      if (!tooltip._pm7TooltipInstance || !PM7Tooltip.instances.has(tooltip)) {
        tooltip.setAttribute('data-pm7-tooltip-healing', 'true');
        console.log('[PM7Tooltip] Healing tooltip:', tooltip);
        new PM7Tooltip(tooltip);
        tooltip.removeAttribute('data-pm7-tooltip-healing');
      }
    });
  }

  // Self-healing function for sidebars
  function healSidebars() {
    // Find all sidebars that were initialized but lost their instance
    const lostSidebars = document.querySelectorAll('[data-pm7-sidebar][data-pm7-initialized]:not([data-pm7-sidebar-healing])');
    
    lostSidebars.forEach(sidebar => {
      if (!sidebar._pm7SidebarInstance || !PM7Sidebar.instances.has(sidebar)) {
        sidebar.setAttribute('data-pm7-sidebar-healing', 'true');
        console.log('[PM7Sidebar] Healing sidebar:', sidebar);
        new PM7Sidebar(sidebar);
        sidebar.removeAttribute('data-pm7-sidebar-healing');
      }
    });
  }

  // Global PM7 object with helper functions
  const PM7 = {
    // Initialize all PM7 components on the page
    init(container = document, options = {}) {
      console.log('[PM7] Initializing all components...');
      
      // Options for better framework integration
      const {
        delay = 0,           // Delay before initialization (useful for React)
        force = false,       // Force re-initialization even if already initialized
        heal = true         // Run healing after initialization
      } = options;
      
      // Function to run the actual initialization
      const runInit = () => {
        // If force is true, remove all initialization markers first
        if (force) {
          container.querySelectorAll('[data-pm7-menu-initialized]').forEach(el => el.removeAttribute('data-pm7-menu-initialized'));
          container.querySelectorAll('[data-pm7-dialog-initialized]').forEach(el => el.removeAttribute('data-pm7-dialog-initialized'));
          container.querySelectorAll('[data-pm7-tab-initialized]').forEach(el => el.removeAttribute('data-pm7-tab-initialized'));
          container.querySelectorAll('[data-pm7-tooltip-initialized]').forEach(el => el.removeAttribute('data-pm7-tooltip-initialized'));
          container.querySelectorAll('[data-pm7-accordion-initialized]').forEach(el => el.removeAttribute('data-pm7-accordion-initialized'));
          container.querySelectorAll('[data-pm7-theme-switch-initialized]').forEach(el => el.removeAttribute('data-pm7-theme-switch-initialized'));
          container.querySelectorAll('[data-pm7-initialized]').forEach(el => el.removeAttribute('data-pm7-initialized'));
        }
        
        // Initialize menus
      const menus = container.querySelectorAll('[data-pm7-menu]:not([data-pm7-menu-initialized])');
      menus.forEach(menu => {
        new PM7Menu(menu);
        menu.setAttribute('data-pm7-menu-initialized', 'true');
      });
      
      // Initialize dialogs - also handle auto-init for new dialogs
      autoInitDialogs(); // First auto-init any new dialogs
      const dialogs = container.querySelectorAll('[data-pm7-dialog]:not([data-pm7-dialog-initialized])');
      dialogs.forEach(dialog => {
        new PM7Dialog(dialog);
        dialog.setAttribute('data-pm7-dialog-initialized', 'true');
      });
      
      // Initialize buttons with special features
      initButtons();
      
      // Initialize tab selectors
      const tabSelectors = container.querySelectorAll('[data-pm7-tab-selector]:not([data-pm7-tab-initialized])');
      tabSelectors.forEach(tabSelector => {
        new PM7TabSelector(tabSelector);
        tabSelector.setAttribute('data-pm7-tab-initialized', 'true');
      });
      
      // Initialize tooltips
      const tooltips = container.querySelectorAll('[data-pm7-tooltip]:not([data-pm7-tooltip-initialized])');
      tooltips.forEach(tooltip => {
        // Skip if this element is already part of a tooltip structure
        if (!tooltip.classList.contains('pm7-tooltip-trigger') && !tooltip.classList.contains('pm7-tooltip-content')) {
          new PM7Tooltip(tooltip);
          // Note: data-pm7-tooltip-initialized is set by the constructor
        }
      });
      
      // Initialize accordions
      const accordions = container.querySelectorAll('[data-pm7-accordion]:not([data-pm7-accordion-initialized])');
      accordions.forEach(accordion => {
        new PM7Accordion(accordion);
        accordion.setAttribute('data-pm7-accordion-initialized', 'true');
      });
      
      // Initialize theme switches
      const themeSwitches = container.querySelectorAll('[data-pm7-theme-switch]:not([data-pm7-theme-switch-initialized])');
      themeSwitches.forEach(themeSwitch => {
        new PM7ThemeSwitch(themeSwitch);
        themeSwitch.setAttribute('data-pm7-theme-switch-initialized', 'true');
      });
      
      // Initialize sidebars
      const sidebars = container.querySelectorAll('[data-pm7-sidebar]:not([data-pm7-initialized])');
      sidebars.forEach(sidebar => {
        new PM7Sidebar(sidebar);
        sidebar.setAttribute('data-pm7-initialized', 'true');
      });
      
      // Also run healing for components that might have been re-rendered
      if (heal) {
        healMenus();
        healAccordions();
        healTabSelectors();
        healTooltips();
        healSidebars();
      }
      
      console.log('[PM7] All components initialized');
      };
      
      // Handle delay option for framework timing
      if (delay > 0) {
        setTimeout(runInit, delay);
      } else {
        runInit();
      }
    },
    
    // Convenience method for React/Vue with sensible defaults
    initFramework(container = document) {
      return this.init(container, { delay: 50, heal: true });
    },
    
    // Re-initialize all components (useful after dynamic content updates)
    reinit(container = document) {
      console.log('[PM7] Re-initializing all components...');
      return this.init(container, { force: true, heal: true });
    },
    
    // Component constructors for manual initialization
    Menu: PM7Menu,
    Dialog: PM7Dialog,
    Button: PM7Button,
    Toast: PM7Toast,
    TabSelector: PM7TabSelector,
    Tooltip: PM7Tooltip,
    Accordion: PM7Accordion,
    ThemeSwitch: PM7ThemeSwitch,
    Sidebar: PM7Sidebar,
    
    // Utility functions
    showToast,
    closeToast,
    closeAllToasts,
    alert: pm7Alert,
    confirm: pm7Confirm,
    createDialog,
    openDialog,
    closeDialog,
    autoInitDialogs,
    initTooltips,
    
    // Self-healing functions
    healMenus,
    healAccordions,
    healTabSelectors,
    healTooltips,
    healSidebars,
    heal() {
      // Heal all components that support self-healing
      healMenus();
      healAccordions();
      healTabSelectors();
      healTooltips();
      healSidebars();
    }
  };

  // Make PM7 globally available
  if (typeof window !== 'undefined') {
    window.PM7 = PM7;
    
    // Set up periodic self-healing check (for frameworks that don't notify)
    // This helps with React, Vue, and other frameworks that re-render DOM
    if (!window.__PM7_SELF_HEALING_INTERVAL__) {
      window.__PM7_SELF_HEALING_INTERVAL__ = setInterval(() => {
        PM7.heal();
      }, 1000); // Check every second
    }
  }

  exports.PM7 = PM7;
  exports.PM7Accordion = PM7Accordion;
  exports.PM7Button = PM7Button;
  exports.PM7Dialog = PM7Dialog;
  exports.PM7Menu = PM7Menu;
  exports.PM7Sidebar = PM7Sidebar;
  exports.PM7TabSelector = PM7TabSelector;
  exports.PM7ThemeSwitch = PM7ThemeSwitch;
  exports.PM7Toast = PM7Toast;
  exports.PM7Tooltip = PM7Tooltip;
  exports.alert = pm7Alert;
  exports.autoInitDialogs = autoInitDialogs;
  exports.closeAllToasts = closeAllToasts;
  exports.closeDialog = closeDialog;
  exports.closeToast = closeToast;
  exports.confirm = pm7Confirm;
  exports.createDialog = createDialog;
  exports.createHamburgerIcon = createHamburgerIcon;
  exports.createHamburgerIconElement = createHamburgerIconElement;
  exports.default = PM7;
  exports.getHamburgerIconDataURI = getHamburgerIconDataURI;
  exports.initButtons = initButtons;
  exports.initSidebars = initSidebars;
  exports.initTooltips = initTooltips;
  exports.openDialog = openDialog;
  exports.showToast = showToast;

  Object.defineProperty(exports, '__esModule', { value: true });

}));
