const audio = document.querySelector("audio");
const segments = [...document.querySelectorAll(".segment")];
if (audio) {
  document.querySelectorAll("[data-seek]").forEach((button) =>
    button.addEventListener("click", () => {
      audio.currentTime = Number(button.dataset.seek);
      audio.play().catch(() => {});
    }),
  );
  document.querySelector("#speed").addEventListener("change", (event) => {
    audio.playbackRate = Number(event.target.value);
  });
  audio.addEventListener("timeupdate", () => {
    for (const segment of segments)
      segment.classList.toggle(
        "active",
        audio.currentTime >= Number(segment.dataset.start) &&
          audio.currentTime < Number(segment.dataset.end),
      );
  });
}
const search = document.querySelector("#search"),
  speaker = document.querySelector("#speaker");
function filter() {
  let visible = 0;
  const query = search.value.trim().toLocaleLowerCase();
  for (const segment of segments) {
    segment.hidden = !(
      segment.textContent.toLocaleLowerCase().includes(query) &&
      (!speaker.value || speaker.value === segment.dataset.speaker)
    );
    if (!segment.hidden) visible++;
  }
  document.querySelector("#no-results").hidden =
    visible > 0 || segments.length === 0;
}
search?.addEventListener("input", filter);
speaker?.addEventListener("change", filter);
