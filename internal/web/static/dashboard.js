(() => {
  const root = document.documentElement;
  const toggle = document.querySelector('[data-theme-toggle]');
  const icon = document.querySelector('#site-icon');
  const applyTheme = (theme) => {
    root.dataset.theme = theme;
    if (toggle) {
      toggle.textContent = theme === 'dark' ? 'Light mode' : 'Dark mode';
      toggle.setAttribute('aria-label', `Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`);
      toggle.setAttribute('aria-pressed', String(theme === 'light'));
    }
    if (icon) icon.href = theme === 'dark' ? '/static/light-house-white-icon.png' : '/static/light-house-icon.png';
  };
  try {
    applyTheme(localStorage.getItem('lighthouse-theme') === 'light' ? 'light' : 'dark');
  } catch {
    applyTheme('dark');
  }
  toggle?.addEventListener('click', () => {
    const next = root.dataset.theme === 'dark' ? 'light' : 'dark';
    applyTheme(next);
    try { localStorage.setItem('lighthouse-theme', next); } catch { /* Storage may be unavailable. */ }
  });

  document.addEventListener('submit', (event) => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) return;
    const submitter = event.submitter;
    const prompt = submitter?.dataset.confirm || form.dataset.confirm;
    if (form.hasAttribute('data-bulk-form') && !form.querySelector('[name="all_matching"]')) {
      const selected = document.querySelectorAll(`[form="${form.id}"][name="ids"]:checked`).length;
      if (selected === 0) {
        event.preventDefault();
        window.alert('Select at least one item first.');
        return;
      }
    }
    if (!prompt) return;
    const confirmed = window.confirm(prompt);
    if (!confirmed) {
      event.preventDefault();
      return;
    }
    const confirmationField = form.querySelector('input[name="confirm"]');
    if (confirmationField) confirmationField.value = 'yes';
  });
})();
