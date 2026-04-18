// qui-sync frontend — Alpine.js state + REST client.
// Kept intentionally small; real work lives in the API layer.

function quiSyncApp() {
  const boot = window.__QUI_SYNC_BOOTSTRAP__ || { tab: 'export', scale: 'default' };
  return {
    tab: boot.tab,
    scale: boot.scale,
    health: { status: 'idle', version: '' },
    instances: [],
    expanded: {},          // instanceID -> bool
    rulesByInstance: {},   // instanceID -> [rule, ...]
    loadingRules: {},      // instanceID -> bool
    editingCategory: null, // instanceID currently being renamed
    categoryDraft: '',     // new name while editing
    config: null,
    diff: null,
    changelog: null,
    toasts: [],
    nextToastId: 1,
    modal: { open: false, title: '', body: '', confirmLabel: 'Confirm', cancelLabel: 'Cancel', resolve: null },
    // Settings tab — drafts live outside `config` so user can edit without
    // affecting the displayed config until Save is clicked.
    quiDraft: { url: '', apiKey: '' },
    backupDraft: { retention_days: 90, gitignored: true },
    addInstanceOpen: false,
    addInstanceDraft: { qui_instance_id: 0, category: '' },
    availableQuiInstances: [],
    // Sync tab
    subscriptions: [],
    expandedSub: {},
    selectedInstanceBySub: {},
    loadingPlan: {},
    planBySub: {},
    quiRulesCache: {}, // instanceID -> [{qui_rule_id, name}]
    applying: {},
    planCategoryFilter: {},  // slug -> '' (all) or category name
    autoPullInterval: 'off',
    addSubOpen: false,
    addSubDraft: { slug: '', url: '', branch: 'main', auth_mode: 'public', token: '' },
    addSubSSHKey: '', // non-empty after phase-1 generate; empty means phase-1 hasn't run yet
    applyModal: { open: false, slug: '', instanceName: '', create: 0, update: 0, skip: 0 },
    hideDevBanner: localStorage.getItem('qui.hideDevBanner') === 'true',
    hideExportHelp: localStorage.getItem('qui.hideExportHelp') === 'true',
    hideSyncHelp: localStorage.getItem('qui.hideSyncHelp') === 'true',
    loading: { instances: false, export: false, config: false, changelog: false },

    init() {
      this.ping();
      this.loadInstances();
      this.loadConfig();
      this.loadChangelog();
      this.loadSubscriptions();
      setInterval(() => this.ping(), 30000);
    },

    setTab(t) {
      this.tab = t;
      localStorage.setItem('qui.tab', t);
    },
    saveScale() {
      localStorage.setItem('qui.scale', this.scale);
    },

    // Watch for help panel dismissals and persist via localStorage.
    dismissExportHelp() { this.hideExportHelp = true; localStorage.setItem('qui.hideExportHelp', 'true'); },
    dismissSyncHelp()   { this.hideSyncHelp = true;   localStorage.setItem('qui.hideSyncHelp', 'true'); },

    // --- toasts ---

    toast(kind, msg, ttl = 6000) {
      const id = this.nextToastId++;
      this.toasts.push({ id, kind, msg });
      if (ttl > 0) setTimeout(() => this.dismiss(id), ttl);
    },
    dismiss(id) {
      this.toasts = this.toasts.filter((t) => t.id !== id);
    },

    // --- confirm modal (Promise-based) ---

    confirm(opts) {
      return new Promise((resolve) => {
        this.modal = {
          open: true,
          title: opts.title || 'Confirm',
          body: opts.body || '',
          confirmLabel: opts.confirmLabel || 'Confirm',
          cancelLabel: opts.cancelLabel || 'Cancel',
          resolve,
        };
        this.$nextTick(() => {
          const el = this.$refs.modalConfirmBtn;
          if (el) el.focus();
        });
      });
    },
    modalConfirm() {
      const r = this.modal.resolve;
      this.modal = { ...this.modal, open: false, resolve: null };
      if (r) r(true);
    },
    modalCancel() {
      const r = this.modal.resolve;
      this.modal = { ...this.modal, open: false, resolve: null };
      if (r) r(false);
    },

    // --- HTTP helper: unified ok/error handling ---

    async apiFetch(url, opts = {}) {
      let r;
      try {
        r = await fetch(url, opts);
      } catch (e) {
        throw new Error('Network error: ' + e.message);
      }
      const ct = r.headers.get('Content-Type') || '';
      let body;
      if (ct.includes('application/json')) {
        try { body = await r.json(); } catch (_) { body = null; }
      } else {
        body = await r.text();
      }
      if (!r.ok) {
        const msg = (body && body.error) || (typeof body === 'string' && body) || ('HTTP ' + r.status);
        const err = new Error(msg);
        err.status = r.status;
        throw err;
      }
      return body;
    },

    // --- endpoints ---

    async ping() {
      try {
        const j = await this.apiFetch('/api/health');
        this.health = { status: 'ok', version: j.version };
      } catch (_) {
        this.health = { status: 'err', version: '' };
      }
    },

    async loadInstances() {
      this.loading.instances = true;
      try {
        this.instances = await this.apiFetch('/api/instances');
      } catch (e) {
        this.toast('error', 'Failed to load instances: ' + e.message);
      } finally {
        this.loading.instances = false;
      }
    },

    async toggleInstance(id) {
      this.expanded[id] = !this.expanded[id];
      if (this.expanded[id] && !this.rulesByInstance[id]) {
        await this.loadRules(id);
      }
    },

    async loadRules(id) {
      this.loadingRules[id] = true;
      try {
        this.rulesByInstance[id] = await this.apiFetch('/api/instances/' + id + '/rules');
      } catch (e) {
        this.toast('error', 'Failed to load rules for instance ' + id + ': ' + e.message);
      } finally {
        this.loadingRules[id] = false;
      }
    },

    startEditCategory(inst) {
      this.editingCategory = inst.qui_instance_id;
      this.categoryDraft = inst.category;
      this.$nextTick(() => {
        const el = this.$refs.categoryInput;
        if (el) { el.focus(); el.select(); }
      });
    },
    cancelCategoryEdit() {
      this.editingCategory = null;
      this.categoryDraft = '';
    },
    async saveCategory(inst) {
      // @blur fires while we're already navigating away — guard so we
      // don't send a duplicate save.
      if (this.editingCategory !== inst.qui_instance_id) return;
      const newCat = (this.categoryDraft || '').trim();
      const oldCat = inst.category;
      this.editingCategory = null;
      this.categoryDraft = '';
      if (newCat === '' || newCat === oldCat) return;
      if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(newCat)) {
        this.toast('error', 'Invalid folder name — lowercase letters, digits, - and _ only');
        return;
      }
      try {
        await this.apiFetch('/api/instances/' + inst.qui_instance_id + '/category', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ category: newCat }),
        });
        inst.category = newCat;
        this.toast('info', 'Folder renamed: ' + oldCat + ' → ' + newCat);
        // Config on disk changed; refresh Settings view next time.
        this.loadConfig();
      } catch (e) {
        this.toast('error', 'Rename failed: ' + e.message);
      }
    },

    async toggleInstanceExclude(inst) {
      const excluded = !inst.excluded;
      try {
        await this.apiFetch('/api/excludes/instance', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ instance_id: inst.qui_instance_id, excluded }),
        });
        inst.excluded = excluded;
        this.toast('info',
          (excluded ? 'Excluded' : 'Included') + ' entire instance: ' + inst.name);
      } catch (e) {
        this.toast('error', 'Toggle failed: ' + e.message);
      }
    },

    async saveSortOrder(instanceId, rule, newValueStr) {
      const n = parseInt(newValueStr, 10);
      if (isNaN(n)) {
        this.toast('error', 'Priority must be a whole number');
        return;
      }
      // If user sets it back to Qui's value, clear the override instead —
      // keeps state.json tidy and lets future Qui changes flow through.
      const clear = n === rule.qui_sort_order;
      try {
        await this.apiFetch('/api/sort-order', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(
            clear
              ? { instance_id: instanceId, rule_id: rule.qui_id, clear: true }
              : { instance_id: instanceId, rule_id: rule.qui_id, sort_order: n }
          ),
        });
        rule.sort_order = clear ? rule.qui_sort_order : n;
        rule.has_sort_override = !clear;
        // Re-sort the list so reordering is immediately visible.
        this.rulesByInstance[instanceId] = [...this.rulesByInstance[instanceId]]
          .sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name));
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    async resetSortOrder(instanceId, rule) {
      try {
        await this.apiFetch('/api/sort-order', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ instance_id: instanceId, rule_id: rule.qui_id, clear: true }),
        });
        rule.sort_order = rule.qui_sort_order;
        rule.has_sort_override = false;
        this.rulesByInstance[instanceId] = [...this.rulesByInstance[instanceId]]
          .sort((a, b) => a.sort_order - b.sort_order || a.name.localeCompare(b.name));
      } catch (e) {
        this.toast('error', 'Reset failed: ' + e.message);
      }
    },

    async toggleExclude(instanceId, rule) {
      const excluded = !rule.excluded;
      try {
        await this.apiFetch('/api/excludes', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ instance_id: instanceId, rule_id: rule.qui_id, excluded }),
        });
        rule.excluded = excluded;
        const inst = this.instances.find((i) => i.qui_instance_id === instanceId);
        if (inst) inst.excluded_count += excluded ? 1 : -1;
        this.toast('info', (excluded ? 'Excluded: ' : 'Included: ') + rule.name);
      } catch (e) {
        this.toast('error', 'Toggle failed: ' + e.message);
      }
    },

    async includeAll(instanceId) {
      await this.bulkExclude(instanceId, false);
    },
    async excludeAll(instanceId) {
      await this.bulkExclude(instanceId, true);
    },
    async bulkExclude(instanceId, excluded) {
      const rules = this.rulesByInstance[instanceId] || [];
      const toChange = rules.filter((r) => r.excluded !== excluded);
      if (toChange.length === 0) return;
      let ok = 0;
      for (const rule of toChange) {
        try {
          await this.apiFetch('/api/excludes', {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ instance_id: instanceId, rule_id: rule.qui_id, excluded }),
          });
          rule.excluded = excluded;
          ok++;
        } catch (_) { /* continue; error toast happens once at end */ }
      }
      const inst = this.instances.find((i) => i.qui_instance_id === instanceId);
      if (inst) {
        inst.excluded_count = rules.filter((r) => r.excluded).length;
      }
      this.toast('info', (excluded ? 'Excluded' : 'Included') + ' ' + ok + ' rule' + (ok === 1 ? '' : 's'));
    },

    async loadConfig() {
      this.loading.config = true;
      try {
        this.config = await this.apiFetch('/api/config');
        this.resetQuiDraft();
        this.resetBackupDraft();
        this.resetAutoPullInterval();
      } catch (e) {
        this.toast('error', 'Failed to load config: ' + e.message);
      } finally {
        this.loading.config = false;
      }
    },

    resetAutoPullInterval() {
      if (this.config) this.autoPullInterval = this.config.auto_pull_interval || 'off';
    },

    async saveAutoPullInterval() {
      try {
        await this.apiFetch('/api/config/auto-pull-interval', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ interval: this.autoPullInterval }),
        });
        this.toast('info', 'Auto-pull interval: ' + this.autoPullInterval);
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    resetQuiDraft() {
      if (!this.config) return;
      this.quiDraft = { url: this.config.qui.url || '', apiKey: '' };
    },
    resetBackupDraft() {
      if (!this.config) return;
      this.backupDraft = {
        retention_days: this.config.backup.retention_days || 0,
        gitignored: !!this.config.backup.gitignored,
      };
    },

    async saveQuiConfig() {
      this.loading.config = true;
      try {
        const body = { url: this.quiDraft.url };
        if (this.quiDraft.apiKey) body.api_key = this.quiDraft.apiKey;
        await this.apiFetch('/api/config/qui', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        await this.loadConfig();
        await this.loadInstances();
        this.toast('info', 'Qui connection saved.');
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      } finally {
        this.loading.config = false;
      }
    },

    async saveBackupConfig() {
      try {
        await this.apiFetch('/api/config/backup', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(this.backupDraft),
        });
        await this.loadConfig();
        this.toast('info', 'Backup settings saved.');
      } catch (e) {
        this.toast('error', 'Save failed: ' + e.message);
      }
    },

    async openAddInstanceModal() {
      this.addInstanceDraft = { qui_instance_id: 0, category: '' };
      try {
        this.availableQuiInstances = await this.apiFetch('/api/qui-instances');
      } catch (e) {
        this.toast('error', 'Could not reach Qui: ' + e.message);
        return;
      }
      this.addInstanceOpen = true;
    },

    async addInstance() {
      const d = this.addInstanceDraft;
      if (!d.qui_instance_id || !d.category) return;
      try {
        await this.apiFetch('/api/instances', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(d),
        });
        this.addInstanceOpen = false;
        await this.loadConfig();
        await this.loadInstances();
        this.toast('info', 'Instance added.');
      } catch (e) {
        this.toast('error', 'Add failed: ' + e.message);
      }
    },

    // ============ Sync tab methods ============

    async loadSubscriptions() {
      try {
        this.subscriptions = await this.apiFetch('/api/subscriptions');
        // Pre-initialize per-slug reactive keys so Alpine detects changes
        // when the user interacts with dropdowns and buttons. Without this,
        // adding a new key to a proxy object doesn't trigger re-evaluation
        // of :disabled and other bindings that read the key.
        for (const sub of this.subscriptions) {
          if (!(sub.slug in this.selectedInstanceBySub)) this.selectedInstanceBySub[sub.slug] = 0;
          if (!(sub.slug in this.expandedSub)) this.expandedSub[sub.slug] = false;
          if (!(sub.slug in this.loadingPlan)) this.loadingPlan[sub.slug] = false;
          if (!(sub.slug in this.applying)) this.applying[sub.slug] = false;
          if (!(sub.slug in this.planCategoryFilter)) this.planCategoryFilter[sub.slug] = '';
        }
      } catch (e) {
        this.toast('error', 'Load subscriptions: ' + e.message);
      }
    },

    toggleSub(slug) {
      this.expandedSub[slug] = !this.expandedSub[slug];
    },

    openAddSubModal() {
      this.addSubDraft = { slug: '', url: '', branch: 'main', auth_mode: 'public', token: '' };
      this.addSubSSHKey = '';
      this.addSubOpen = true;
    },

    cancelAddSub() {
      this.addSubOpen = false;
      this.addSubSSHKey = '';
    },

    // Button label reflects the current phase of the flow.
    addSubNextLabel() {
      if (this.addSubDraft.auth_mode === 'ssh_deploy_key' && !this.addSubSSHKey) {
        return 'Generate deploy key';
      }
      return 'Add';
    },

    async addSubscription() {
      const d = this.addSubDraft;
      if (!d.slug || !d.url) return;
      if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(d.slug)) {
        this.toast('error', 'Invalid name — use lowercase letters, digits, hyphens and underscores only');
        return;
      }

      // Phase 1 — SSH mode, no key generated yet: create the key, show
      // it inline, and wait for the user to paste it into GitHub before
      // they click Add again.
      if (d.auth_mode === 'ssh_deploy_key' && !this.addSubSSHKey) {
        try {
          const r = await this.apiFetch('/api/subscriptions/generate-key', {
            method: 'POST', headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ slug: d.slug }),
          });
          this.addSubSSHKey = r.public;
          this.toast('info', 'Deploy key generated — paste into GitHub, then click Add.');
        } catch (e) {
          this.toast('error', 'Key generation failed: ' + e.message);
        }
        return;
      }

      // Phase 2 — actually register the subscription.
      try {
        await this.apiFetch('/api/subscriptions', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(d),
        });
        this.addSubOpen = false;
        this.addSubSSHKey = '';
        await this.loadSubscriptions();
        this.toast('info', 'Subscription added: ' + d.slug);
      } catch (e) {
        this.toast('error', 'Add failed: ' + e.message);
      }
    },

    async copyAddSubKey() {
      try {
        await navigator.clipboard.writeText(this.addSubSSHKey);
        this.toast('info', 'Public key copied.');
      } catch (e) {
        this.toast('error', 'Clipboard unavailable — select manually.');
      }
    },

    async removeSub(sub) {
      const ok = await this.confirm({
        title: 'Remove subscription?',
        body: 'Remove "' + sub.slug + '"? The local clone and any SSH keys will be deleted. Your Qui rules are untouched.',
        confirmLabel: 'Remove',
      });
      if (!ok) return;
      try {
        await this.apiFetch('/api/subscriptions/' + sub.slug, { method: 'DELETE' });
        await this.loadSubscriptions();
        delete this.expandedSub[sub.slug];
        delete this.planBySub[sub.slug];
        this.toast('info', 'Subscription removed.');
      } catch (e) {
        this.toast('error', 'Remove failed: ' + e.message);
      }
    },

    async pullSub(slug) {
      try {
        const r = await this.apiFetch('/api/subscriptions/' + slug + '/pull', { method: 'POST' });
        await this.loadSubscriptions();
        this.toast('info', 'Pulled ' + slug + ' — HEAD ' + (r.sha || '').slice(0,7));
      } catch (e) {
        this.toast('error', 'Pull failed: ' + e.message);
      }
    },

    async loadQuiRulesFor(instanceId) {
      if (this.quiRulesCache[instanceId]) return;
      try {
        this.quiRulesCache[instanceId] = await this.apiFetch('/api/instances/' + instanceId + '/qui-rules');
      } catch (e) {
        this.quiRulesCache[instanceId] = [];
      }
    },

    async planSub(slug) {
      const instanceId = this.selectedInstanceBySub[slug];
      if (!instanceId) return;
      this.loadingPlan[slug] = true;
      try {
        // Load Qui rules for link-existing dropdown in parallel with plan.
        await this.loadQuiRulesFor(instanceId);
        const plan = await this.apiFetch('/api/subscriptions/' + slug + '/plan', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ qui_instance_id: instanceId }),
        });
        this.planBySub[slug] = plan;
      } catch (e) {
        this.toast('error', 'Plan failed: ' + e.message);
      } finally {
        this.loadingPlan[slug] = false;
      }
    },

    selectAllRules(slug, include) {
      const rules = this.filteredPlanRules(slug);
      for (const rule of rules) {
        if (include && rule.action === 'skip') {
          if (rule.link_suggestion && rule.link_suggestion.qui_rule_id) {
            rule.action = 'update_existing';
            rule.qui_rule_id = rule.link_suggestion.qui_rule_id;
          } else {
            rule.action = 'create_new';
          }
        } else if (!include && rule.action !== 'skip') {
          rule.action = 'skip';
        }
      }
    },

    planCategories(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return [];
      const cats = new Set();
      for (const r of plan.rules) if (r.category) cats.add(r.category);
      return [...cats].sort();
    },

    filteredPlanRules(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return [];
      const cat = this.planCategoryFilter[slug];
      if (!cat) return plan.rules;
      return plan.rules.filter((r) => r.category === cat);
    },

    async toggleAutoSync(slug, rule) {
      const autoSync = !rule.auto_sync;
      try {
        await this.apiFetch('/api/subscriptions/' + slug + '/rules/auto-sync', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ repo_path: rule.repo_path, auto_sync: autoSync }),
        });
        rule.auto_sync = autoSync;
      } catch (e) {
        this.toast('error', 'Toggle auto-sync: ' + e.message);
      }
    },

    toggleRuleSkip(slug, rule) {
      if (rule.action === 'skip') {
        // Un-skip: default back to what link_suggestion said.
        if (rule.link_suggestion && rule.link_suggestion.qui_rule_id) {
          rule.action = 'update_existing';
          rule.qui_rule_id = rule.link_suggestion.qui_rule_id;
        } else {
          rule.action = 'create_new';
        }
      } else {
        rule.action = 'skip';
      }
    },

    setRulePriority(slug, rule, value) {
      const n = parseInt(value, 10);
      if (isNaN(n)) return;
      rule.sort_order = n;
      rule.sort_order_from = 'override';
    },

    setRuleLink(slug, rule, value) {
      if (value === 'create_new') {
        rule.action = 'create_new';
        rule.qui_rule_id = 0;
      } else if (value === 'skip') {
        rule.action = 'skip';
      } else if (value.startsWith('u:')) {
        rule.action = 'update_existing';
        rule.qui_rule_id = parseInt(value.slice(2), 10);
      }
    },

    openApplyModal(slug) {
      const plan = this.planBySub[slug];
      if (!plan) return;
      const visible = this.filteredPlanRules(slug);
      const counts = { create_new: 0, update_existing: 0, skip: 0 };
      for (const r of visible) counts[r.action] = (counts[r.action] || 0) + 1;
      const inst = this.instances.find((i) => i.qui_instance_id === plan.qui_instance_id);
      this.applyModal = {
        open: true,
        slug,
        instanceName: inst ? inst.qui_name : ('Instance ' + plan.qui_instance_id),
        create: counts.create_new,
        update: counts.update_existing,
        skip: counts.skip,
      };
    },

    async runApply() {
      const slug = this.applyModal.slug;
      const plan = this.planBySub[slug];
      if (!plan) return;
      this.applyModal.open = false;
      this.applying[slug] = true;
      try {
        const visible = this.filteredPlanRules(slug);
        const decisions = visible.map((r) => ({
          repo_path: r.repo_path,
          action: r.action,
          qui_rule_id: r.action === 'update_existing' ? r.qui_rule_id : 0,
          sort_order_override: r.sort_order_from === 'override' ? r.sort_order : null,
        }));
        const result = await this.apiFetch('/api/subscriptions/' + slug + '/apply', {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ qui_instance_id: plan.qui_instance_id, decisions }),
        });
        const nC = result.created ? result.created.length : 0;
        const nU = result.updated ? result.updated.length : 0;
        const nF = result.failed ? result.failed.length : 0;
        if (nF > 0) {
          this.toast('error', 'Applied with errors: ' + nC + ' created, ' + nU + ' updated, ' + nF + ' failed');
        } else {
          this.toast('info', 'Applied: ' + nC + ' created, ' + nU + ' updated');
        }
        // Refresh plan + instances so counts update.
        await this.planSub(slug);
        await this.loadInstances();
      } catch (e) {
        this.toast('error', 'Apply failed: ' + e.message);
      } finally {
        this.applying[slug] = false;
      }
    },

    async removeInstance(inst) {
      const ok = await this.confirm({
        title: 'Remove instance?',
        body: 'Remove Qui instance #' + inst.qui_instance_id +
              ' (folder: ' + inst.category + ') from config? ' +
              'The folder and existing rules stay on disk — re-add to resume, or delete manually.',
        confirmLabel: 'Remove',
      });
      if (!ok) return;
      try {
        await this.apiFetch('/api/instances/' + inst.qui_instance_id, { method: 'DELETE' });
        await this.loadConfig();
        await this.loadInstances();
        this.toast('info', 'Instance removed.');
      } catch (e) {
        this.toast('error', 'Remove failed: ' + e.message);
      }
    },

    async reloadConfig() {
      this.loading.config = true;
      try {
        await this.apiFetch('/api/config/reload', { method: 'POST' });
        await this.loadConfig();
        await this.loadInstances();
        this.toast('info', 'Config reloaded from disk.');
      } catch (e) {
        this.toast('error', 'Reload failed: ' + e.message);
      } finally {
        this.loading.config = false;
      }
    },

    async loadChangelog() {
      this.loading.changelog = true;
      try {
        this.changelog = await this.apiFetch('/api/changelog');
      } catch (e) {
        this.toast('error', 'Failed to load changelog: ' + e.message);
      } finally {
        this.loading.changelog = false;
      }
    },

    async preview() {
      this.loading.export = true;
      this.diff = null;
      try {
        this.diff = await this.apiFetch('/api/export/preview', { method: 'POST' });
      } catch (e) {
        this.toast('error', 'Preview failed: ' + e.message);
      } finally {
        this.loading.export = false;
      }
    },

    async runExport() {
      const ok = await this.confirm({
        title: 'Commit export?',
        body: 'Write pending changes to the share-repo on disk. Files are backed up before overwrite. This does not push to git — you still commit manually.',
        confirmLabel: 'Commit export',
        cancelLabel: 'Cancel',
      });
      if (!ok) return;
      this.loading.export = true;
      try {
        this.diff = await this.apiFetch('/api/export/run', { method: 'POST' });
        await this.loadChangelog();
        if (this.diff.empty) {
          this.toast('info', 'Nothing to commit — local rules already in sync with Qui.');
        } else {
          const total =
            (this.diff.added   || []).length +
            (this.diff.updated || []).length +
            (this.diff.renamed || []).length +
            (this.diff.removed || []).length +
            (this.diff.moved   || []).length;
          const gitMsg = this.diff.git_committed ? ' + committed to git' : '';
          this.toast('info', 'Export committed — ' + total + ' change' + (total === 1 ? '' : 's') + gitMsg + '.');
        }
      } catch (e) {
        this.toast('error', 'Export failed: ' + e.message);
      } finally {
        this.loading.export = false;
      }
    },
  };
}
