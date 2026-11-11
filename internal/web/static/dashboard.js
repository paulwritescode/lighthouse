(() => {
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
