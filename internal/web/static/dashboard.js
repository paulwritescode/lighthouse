(() => {
  document.addEventListener('click', async (event) => {
    if (!(event.target instanceof Element)) return;
    const button = event.target.closest('[data-copy-path]');
    if (!(button instanceof HTMLButtonElement)) return;
    const path = button.dataset.copyPath || '';
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(path);
      } else {
        const input = document.createElement('textarea');
        input.value = path;
        input.setAttribute('readonly', '');
        input.style.position = 'fixed';
        input.style.opacity = '0';
        document.body.append(input);
        input.select();
        const copied = document.execCommand('copy');
        input.remove();
        if (!copied) throw new Error('Clipboard unavailable');
      }
      button.textContent = 'Copied';
      window.setTimeout(() => { button.textContent = 'Copy path'; }, 1600);
    } catch {
      button.textContent = 'Copy failed';
      window.setTimeout(() => { button.textContent = 'Copy path'; }, 2000);
    }
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
    if (!window.confirm(prompt)) {
      event.preventDefault();
      return;
    }
    const confirmationField = form.querySelector('input[name="confirm"]');
    if (confirmationField) confirmationField.value = 'yes';
  });
})();
