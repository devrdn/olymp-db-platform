/**
 * The product's marks, copied shape for shape from the owner's design canvas.
 *
 * Each shape names its paint. `ink` and `chalk` are the black and the cream
 * patches, which trade places on a dark ground; `ink-line` is a stroke in ink;
 * every other paint is a key of PAINT and is the same in both themes. Plain
 * JavaScript, so the icon script can import it as well as the app.
 *
 * @typedef {{ el: "path" | "polygon" | "rect" | "ellipse", attrs: Record<string, string>, paint: string }} Shape
 * @typedef {{ viewBox: string, shapes: Shape[] }} Mark
 */

/** The colours that do not change with the theme. */
export const PAINT = {
  terracotta: "#E0805F",
  green: "#6B9E57",
  mustard: "#E8B24A",
  red: "#C8392E",
  pupil: "#1E1A18",
};

/** The width of the stroke that `ink-line` draws, in the mark's own units. */
export const LINE_WIDTH = 1.2;

/**
 * The logo: the cat on its pedestal. Its smallest size is 32 px high.
 *
 * @type {Mark}
 */
export const CAT = {
  viewBox: "16 8 90 116",
  shapes: [
    { el: "path", attrs: { d: "M28,92 A32,7 0 0 0 92,92 L92,102 A32,7 0 0 1 28,102 Z" }, paint: "green" },
    { el: "path", attrs: { d: "M28,105 A32,7 0 0 0 92,105 L92,114 A32,7 0 0 1 28,114 Z" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "36.00,99.53 38.10,101.63 36.00,103.73 33.90,101.63" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "48.00,101.39 50.10,103.49 48.00,105.59 45.90,103.49" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "60.00,101.90 62.10,104.00 60.00,106.10 57.90,104.00" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "72.00,101.39 74.10,103.49 72.00,105.59 69.90,103.49" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "84.00,99.53 86.10,101.63 84.00,103.73 81.90,101.63" }, paint: "chalk" },
    { el: "ellipse", attrs: { cx: "60", cy: "92", rx: "32", ry: "7" }, paint: "ink" },
    { el: "polygon", attrs: { points: "37,88 31,85 29,91 36,94" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "31,85 26,79 22,84 29,91" }, paint: "ink" },
    { el: "polygon", attrs: { points: "26,79 25,71 19,72 22,84" }, paint: "green" },
    { el: "polygon", attrs: { points: "25,71 27,63 21,61 19,72" }, paint: "mustard" },
    { el: "polygon", attrs: { points: "27,63 25,56 21,61" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "36,94 37,78 46,62 55,47 70,44 74,58 73,94" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "36,94 37,78 46,62 52,72 48,94" }, paint: "ink" },
    { el: "polygon", attrs: { points: "48,94 52,82 61,86 59,94" }, paint: "green" },
    { el: "polygon", attrs: { points: "62,48 70,46 71,59 66,66 62,58" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "63,90 73,89 75,94 62,94" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "54,46 53,30 55,14 62,24 70,12 73,24 86,28 82,36 70,44" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "54,46 53,30 55,14 62,24 64,34 58,44" }, paint: "ink" },
    { el: "polygon", attrs: { points: "70,12 73,24 68,21" }, paint: "mustard" },
    { el: "path", attrs: { d: "M67,27 Q71,23.5 75,26.5 Q71,29.5 67,27Z" }, paint: "mustard" },
    { el: "rect", attrs: { x: "70.6", y: "24.6", width: "1.1", height: "4" }, paint: "pupil" },
    { el: "polygon", attrs: { points: "83.5,27.6 86.5,28.2 84.6,30.6" }, paint: "red" },
    { el: "polygon", attrs: { points: "54,45 70,42 71,46 55,49" }, paint: "red" },
    { el: "polygon", attrs: { points: "66.0,51.2 66.7,53.3 68.7,52.3 67.7,54.3 69.8,55.0 67.7,55.7 68.7,57.7 66.7,56.7 66.0,58.8 65.3,56.7 63.3,57.7 64.3,55.7 62.2,55.0 64.3,54.3 63.3,52.3 65.3,53.3" }, paint: "red" },
    { el: "path", attrs: { d: "M80,32 L102,27 M80,34 L103,34" }, paint: "ink-line" },
  ],
};

/**
 * The cat's face as the tab icon draws it, simplified for 16 to 48 px.
 *
 * @type {Mark}
 */
export const FACE = {
  viewBox: "0 0 32 32",
  shapes: [
    { el: "polygon", attrs: { points: "6,24 16,28 26,24 27,28 16,32 5,28" }, paint: "red" },
    { el: "polygon", attrs: { points: "3,1 10,8 22,8 29,1 30,15 25,24 16,28 7,24 2,15" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "3,1 10,8 13,8 11,16 2,15" }, paint: "ink" },
    { el: "polygon", attrs: { points: "19,19 30,15 25,24 17,27" }, paint: "green" },
    { el: "polygon", attrs: { points: "25,5 29,1 29.5,10" }, paint: "mustard" },
    { el: "polygon", attrs: { points: "16,9 18.5,11.5 16,14 13.5,11.5" }, paint: "chalk" },
    { el: "rect", attrs: { x: "7", y: "16", width: "6", height: "3" }, paint: "mustard" },
    { el: "rect", attrs: { x: "19", y: "16", width: "6", height: "3" }, paint: "mustard" },
    { el: "rect", attrs: { x: "9.5", y: "15.5", width: "1.5", height: "4" }, paint: "pupil" },
    { el: "rect", attrs: { x: "21.5", y: "15.5", width: "1.5", height: "4" }, paint: "pupil" },
    { el: "polygon", attrs: { points: "14,21 18,21 16,23.5" }, paint: "red" },
  ],
};

/**
 * The cat's face in full, for the home-screen icon.
 *
 * @type {Mark}
 */
export const FACE_DETAILED = {
  viewBox: "8 6 84 92",
  shapes: [
    { el: "polygon", attrs: { points: "22,72 50,84 78,72 80,82 50,96 20,82" }, paint: "red" },
    { el: "polygon", attrs: { points: "31,80 33,82 31,84 29,82" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "41,85 43,87 41,89 39,87" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "59,85 61,87 59,89 57,87" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "69,80 71,82 69,84 67,82" }, paint: "chalk" },
    { el: "polygon", attrs: { points: "16,10 34,28 66,28 84,10 88,46 76,72 50,84 24,72 12,46" }, paint: "terracotta" },
    { el: "polygon", attrs: { points: "16,10 34,28 42,28 36,50 12,46" }, paint: "ink" },
    { el: "polygon", attrs: { points: "60,58 88,46 76,72 56,80" }, paint: "green" },
    { el: "polygon", attrs: { points: "76,18 84,10 85,30" }, paint: "mustard" },
    { el: "polygon", attrs: { points: "50.0,28.0 51.4,32.6 55.7,30.3 53.4,34.6 58.0,36.0 53.4,37.4 55.7,41.7 51.4,39.4 50.0,44.0 48.6,39.4 44.3,41.7 46.6,37.4 42.0,36.0 46.6,34.6 44.3,30.3 48.6,32.6" }, paint: "chalk" },
    { el: "path", attrs: { d: "M26,53 Q33,47 40,53 Q33,58 26,53Z" }, paint: "mustard" },
    { el: "path", attrs: { d: "M60,53 Q67,47 74,53 Q67,58 60,53Z" }, paint: "mustard" },
    { el: "rect", attrs: { x: "32.1", y: "49.5", width: "1.8", height: "7" }, paint: "pupil" },
    { el: "rect", attrs: { x: "66.1", y: "49.5", width: "1.8", height: "7" }, paint: "pupil" },
    { el: "polygon", attrs: { points: "45,64 55,64 50,69" }, paint: "red" },
  ],
};

/**
 * A mark as a standalone SVG document. Its theme patches are painted in
 * `scheme`'s colours; given `dark` as well, they are painted by class and swap
 * to `dark`'s under a dark colour scheme, which is how a tab icon follows the
 * browser.
 *
 * @param {Mark} mark
 * @param {{ ink: string, chalk: string }} scheme
 * @param {{ ink: string, chalk: string }} [dark]
 * @returns {string}
 */
export function markSvg(mark, scheme, dark) {
  const line = `fill="none" stroke-width="${LINE_WIDTH}" stroke-linecap="round"`;
  const paint = (name) => {
    if (dark && (name === "ink" || name === "chalk")) return `class="${name}"`;
    if (dark && name === "ink-line") return `class="ink-line" ${line}`;
    if (name === "ink-line") return `stroke="${scheme.ink}" ${line}`;
    return `fill="${name === "ink" ? scheme.ink : name === "chalk" ? scheme.chalk : PAINT[name]}"`;
  };
  const rules = (colours) => `.ink{fill:${colours.ink}}.chalk{fill:${colours.chalk}}.ink-line{stroke:${colours.ink}}`;
  const style = dark ? `<style>${rules(scheme)}@media (prefers-color-scheme: dark){${rules(dark)}}</style>` : "";
  const shapes = mark.shapes.map(({ el, attrs, paint: name }) => {
    const attributes = Object.entries(attrs).map(([key, value]) => `${key}="${value}"`);
    return `<${el} ${attributes.join(" ")} ${paint(name)}/>`;
  });
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="${mark.viewBox}">${style}${shapes.join("")}</svg>`;
}
