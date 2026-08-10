// Home page script: the share dialog is all there is to wire.
import { wireShareDialog } from "../shared/share-dialog";

document.getElementById("share-open")!.addEventListener("click", wireShareDialog());
