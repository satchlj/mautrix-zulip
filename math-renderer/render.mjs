// Renders LaTeX math to PNG for mautrix-zulip.
//
// Protocol: one JSON object per line on stdin, {"id": 1, "tex": "x^2", "display": false},
// and one JSON object per line on stdout, {"id": 1, "png": "<base64>", "width": 20, "height": 12}
// or {"id": 1, "error": "..."}. Width and height are the intended display size in CSS pixels;
// the PNG itself is rendered at a higher resolution.
import { createInterface } from "node:readline";
import { Resvg } from "@resvg/resvg-js";
import { mathjax } from "mathjax-full/js/mathjax.js";
import { TeX } from "mathjax-full/js/input/tex.js";
import { SVG } from "mathjax-full/js/output/svg.js";
import { liteAdaptor } from "mathjax-full/js/adaptors/liteAdaptor.js";
import { RegisterHTMLHandler } from "mathjax-full/js/handlers/html.js";
import { AllPackages } from "mathjax-full/js/input/tex/AllPackages.js";

const EX_PX = Number(process.env.MATH_EX_PX || 8);
const SCALE = Number(process.env.MATH_SCALE || 3);
const FOREGROUND = process.env.MATH_FOREGROUND || "#000000";
const BACKGROUND = process.env.MATH_BACKGROUND || "#ffffff";
const PADDING_PX = Number(process.env.MATH_PADDING_PX || 2);
// Optional outline color around the glyphs, which keeps transparent images readable on both light and dark backgrounds
const HALO = process.env.MATH_HALO || "";
const HALO_RADIUS = Number(process.env.MATH_HALO_RADIUS || 0.6);

const adaptor = liteAdaptor();
RegisterHTMLHandler(adaptor);
const tex = new TeX({
	// noerrors and noundefined would render errors into the image, but we want to fall back to the source instead
	packages: AllPackages.filter((pkg) => pkg !== "noerrors" && pkg !== "noundefined"),
	formatError: (jax, err) => {
		throw err;
	},
});
const svg = new SVG({ fontCache: "none" });
const doc = mathjax.document("", { InputJax: tex, OutputJax: svg });

const exToPx = (value) => parseFloat(value) * EX_PX;

function render(source, display) {
	const node = doc.convert(source, { display, em: EX_PX * 2, ex: EX_PX, containerWidth: 80 * EX_PX * 2 });
	let svgText = adaptor.innerHTML(node);
	const width = exToPx(/ width="([\d.]+)ex"/.exec(svgText)[1]);
	const height = exToPx(/ height="([\d.]+)ex"/.exec(svgText)[1]);
	svgText = svgText
		.replace(/ width="[\d.]+ex"/, ` width="${width}"`)
		.replace(/ height="[\d.]+ex"/, ` height="${height}"`)
		.replaceAll("currentColor", FOREGROUND);
	// Wrap in another SVG to add padding around the formula
	const outerWidth = width + PADDING_PX * 2;
	const outerHeight = height + PADDING_PX * 2;
	const halo = HALO
		? `<defs><filter id="halo" x="-10%" y="-10%" width="120%" height="120%"><feMorphology in="SourceAlpha" operator="dilate" radius="${HALO_RADIUS}" result="dilated"/><feFlood flood-color="${HALO}"/><feComposite in2="dilated" operator="in" result="outline"/><feMerge><feMergeNode in="outline"/><feMergeNode in="SourceGraphic"/></feMerge></filter></defs>`
		: "";
	const filterAttr = HALO ? ` filter="url(#halo)"` : "";
	const padded = `<svg xmlns="http://www.w3.org/2000/svg" width="${outerWidth}" height="${outerHeight}">${halo}<g transform="translate(${PADDING_PX} ${PADDING_PX})"${filterAttr}>${svgText}</g></svg>`;
	const png = new Resvg(padded, {
		fitTo: { mode: "zoom", value: SCALE },
		// Leave the background transparent if requested
		...(BACKGROUND && BACKGROUND !== "transparent" ? { background: BACKGROUND } : {}),
	})
		.render()
		.asPng();
	return { png: Buffer.from(png).toString("base64"), width: Math.ceil(outerWidth), height: Math.ceil(outerHeight) };
}

const rl = createInterface({ input: process.stdin });
rl.on("line", (line) => {
	let req;
	try {
		req = JSON.parse(line);
	} catch (err) {
		return;
	}
	let resp;
	try {
		resp = { id: req.id, ...render(req.tex, !!req.display) };
	} catch (err) {
		resp = { id: req.id, error: String(err?.message || err) };
	}
	process.stdout.write(JSON.stringify(resp) + "\n");
});
