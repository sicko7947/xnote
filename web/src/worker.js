import { createHandler } from "./handler.js";
import style from "./style.css";
import app from "./app.js";
export default { fetch: createHandler({ style, app }) };
