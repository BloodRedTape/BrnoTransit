document.addEventListener("DOMContentLoaded", () => {
  const swap = document.getElementById("swap");
  if (!swap) return;
  swap.addEventListener("click", () => {
    const from = document.getElementById("from");
    const to = document.getElementById("to");
    [from.value, to.value] = [to.value, from.value];
  });
});
